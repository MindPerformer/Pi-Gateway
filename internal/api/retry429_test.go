package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pi-gateway/internal/store"
)

func Test429AccountSwitchAndCooldown(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		global                                       bool
		group                                        string
		stream, eventError, priorOutput, exhaust, ws bool
		attempts, wantCalls, wantStatus              int
	}{
		{name: "default enabled", global: true, attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "websocket", global: true, ws: true, attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "stream", global: true, stream: true, attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "event error", global: true, stream: true, eventError: true, attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "global disabled", attempts: 2, wantCalls: 1, wantStatus: 429},
		{name: "group enables", group: "enabled", attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "group disables", global: true, group: "disabled", attempts: 2, wantCalls: 1, wantStatus: 429},
		{name: "group inherits", global: true, group: "inherit", attempts: 2, wantCalls: 2, wantStatus: 200},
		{name: "attempt limit", global: true, attempts: 1, wantCalls: 1, wantStatus: 429},
		{name: "all limited", global: true, exhaust: true, attempts: 4, wantCalls: 2, wantStatus: 429},
		{name: "already output", global: true, stream: true, priorOutput: true, eventError: true, attempts: 2, wantCalls: 1, wantStatus: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var called []string
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				called = append(called, r.Header.Get("Authorization"))
				n := len(called)
				mu.Unlock()
				if n == 1 || tc.exhaust {
					w.Header().Set("Retry-After", "90")
					if tc.eventError {
						w.Header().Set("Content-Type", "text/event-stream")
						if tc.priorOutput {
							_, _ = io.WriteString(w, deltaSSE)
						}
						_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n")
					} else {
						w.WriteHeader(429)
						_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"limited"}}`)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, terminalSSE)
			}), "sse")
			ctx := context.Background()
			a := &store.Account{Name: "second", AccountID: "acct-second", AccessToken: fakeJWT(t, "acct-second"), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Enabled: true, Weight: 1, Concurrency: 3, Status: store.AccountStatusReady}
			if err := h.store.CreateAccount(ctx, a); err != nil {
				t.Fatal(err)
			}
			// Both accounts use their own 120-second floor, overriding the global 60.
			seconds := 120
			for _, id := range []int64{h.accountID, a.ID} {
				if err := h.store.PatchAccountManagementFields(ctx, id, store.AccountManagementPatch{Cooldown429Seconds: &seconds}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.group != "" {
				g := &store.AccountGroup{Name: "policy", Enabled: true, AccountIDs: []int64{h.accountID, a.ID}, SwitchOn429: tc.group}
				if err := h.store.CreateAccountGroup(ctx, g); err != nil {
					t.Fatal(err)
				}
			}
			rt := h.dataPlane.settings.Get()
			rt.SwitchOn429 = tc.global
			rt.MaxAttempts = tc.attempts
			if err := h.dataPlane.settings.Set(ctx, rt); err != nil {
				t.Fatal(err)
			}
			h.dataPlane.accounts.SetSettings(rt)
			body := `{"model":"test","input":[],"stream":false}`
			if tc.stream {
				body = `{"model":"test","input":[],"stream":true}`
			}
			req, _ := http.NewRequest("POST", h.server.URL+"/v1/responses", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+h.key)
			before := time.Now()
			if tc.ws {
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + h.key}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "test", "input": []any{}}); err != nil {
					t.Fatal(err)
				}
				_, raw, err := conn.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), "response.completed") {
					t.Fatalf("ws output=%s", raw)
				}
				mu.Lock()
				ids := append([]string(nil), called...)
				mu.Unlock()
				if len(ids) != 2 || ids[0] == ids[1] {
					t.Fatalf("ws attempts=%v", ids)
				}
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.wantStatus, raw)
			}
			mu.Lock()
			ids := append([]string(nil), called...)
			mu.Unlock()
			if len(ids) != tc.wantCalls {
				t.Fatalf("calls=%v want=%d", ids, tc.wantCalls)
			}
			if len(ids) > 1 && ids[0] == ids[1] {
				t.Fatalf("same account retried: %v", ids)
			}
			if tc.wantStatus == 200 && !tc.priorOutput && strings.Contains(string(raw), "limited") {
				t.Fatalf("failed attempt leaked: %s", raw)
			}
			all, err := h.store.ListAccounts(ctx)
			var first *store.Account
			for _, a := range all {
				if "Bearer "+a.AccessToken == ids[0] {
					first = a
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if first == nil {
				t.Fatal("attempt account not found")
			}
			if first.CooldownUntil < before.Add(120*time.Second).UnixMilli() {
				t.Fatalf("cooldown not recorded: %+v", first)
			}
			if h.dataPlane.accounts.Inflight(h.accountID) != 0 || h.dataPlane.accounts.Inflight(a.ID) != 0 {
				t.Fatal("account slots leaked")
			}
		})
	}
}

func TestRetryAfterDuration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{"Retry-After": {"7"}, "Retry-After-Ms": {"8000"}}, 8 * time.Second},
		{http.Header{"Retry-After": {now.Add(time.Minute).Format(http.TimeFormat)}}, time.Minute},
		{http.Header{"Retry-After": {"invalid"}, "Retry-After-Ms": {"-1"}}, 0},
	} {
		if got := retryAfterDuration(tc.h, now); got != tc.want {
			t.Fatalf("delay=%v want=%v", got, tc.want)
		}
	}
}

func Test429GroupOverrideScopeAndConflict(t *testing.T) {
	h := newHarness(t, sseUpstream(&upstreamCapture{}), "sse")
	ctx := context.Background()
	enabled := &store.AccountGroup{Name: "enable", Enabled: true, AccountIDs: []int64{h.accountID}, SwitchOn429: "enabled"}
	disabled := &store.AccountGroup{Name: "disable", Enabled: true, AccountIDs: []int64{h.accountID}, SwitchOn429: "disabled"}
	for _, g := range []*store.AccountGroup{enabled, disabled} {
		if err := h.store.CreateAccountGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	a, err := h.store.GetAccount(ctx, h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	rt := h.dataPlane.settings.Get()
	rt.SwitchOn429 = false
	if err := h.dataPlane.settings.Set(ctx, rt); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ids  []int64
		want bool
	}{
		{nil, false},
		{[]int64{enabled.ID}, true},
		{[]int64{disabled.ID}, false},
		{[]int64{enabled.ID, disabled.ID}, false},
	} {
		p := &prepared{Account: a, Key: &store.APIKey{GroupIDs: tc.ids}}
		if got := h.dataPlane.switchOn429(ctx, p); got != tc.want {
			t.Fatalf("scope=%v switch=%v want=%v", tc.ids, got, tc.want)
		}
	}
	// A disabled group neither authorizes nor imposes an override.
	off := false
	if err := h.store.PatchAccountGroup(ctx, disabled.ID, store.AccountGroupPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if !h.dataPlane.switchOn429(ctx, &prepared{Account: a, Key: &store.APIKey{}}) {
		t.Fatal("disabled group imposed override")
	}
}
