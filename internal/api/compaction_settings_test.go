package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/store"
)

const summaryTestSSE = `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Keep the task goal."}]}],"usage":{"input_tokens":30,"output_tokens":8,"total_tokens":38}}}` + "\n\n"

func TestCompactionModesAndModels(t *testing.T) {
	for _, tc := range []struct {
		name, mode, model        string
		nativeStatus, wantStatus int
		native, summary          int32
	}{
		{"default", "", "gpt-6-luna", 403, 200, 0, 1},
		{"on-custom", "on", "summary-model", 403, 200, 0, 1},
		{"auto-native", "auto", "summary-model", 200, 200, 1, 0},
		{"auto-fallback", "auto", "summary-model", 403, 200, 1, 1},
		{"off-denied", "off", "summary-model", 403, 403, 1, 0},
		{"off-native", "off", "summary-model", 200, 200, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var native, summary atomic.Int32
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if strings.HasSuffix(r.URL.Path, "/compact") {
					native.Add(1)
					if body["model"] != "client-model" {
						t.Errorf("native model changed: %+v", body)
					}
					w.WriteHeader(tc.nativeStatus)
					if tc.nativeStatus == 200 {
						_, _ = io.WriteString(w, compactFixture)
					} else {
						_, _ = io.WriteString(w, `{"error":{"message":"This ChatPass credential is not authorized for the requested operation."}}`)
					}
					return
				}
				summary.Add(1)
				if body["model"] != tc.model {
					t.Errorf("summary model=%v want=%s", body["model"], tc.model)
				}
				if body["tools"] != nil || body["store"] != false {
					t.Errorf("unexpected summary options: %+v", body)
				}
				_, _ = io.WriteString(w, summaryTestSSE)
			}), "sse")
			if tc.mode != "" {
				rt := h.dataPlane.settings.Get()
				rt.CompactionMode, rt.CompactionModel = tc.mode, tc.model
				if err := h.dataPlane.settings.Set(context.Background(), rt); err != nil {
					t.Fatal(err)
				}
			}
			resp, raw := postCompactTest(t, h, "/v1/responses/compact", `{"model":"client-model","input":[],"tools":[{"type":"function","name":"should_not_execute"}]}`)
			if resp.StatusCode != tc.wantStatus || native.Load() != tc.native || summary.Load() != tc.summary {
				t.Fatalf("status=%d native=%d summary=%d body=%s", resp.StatusCode, native.Load(), summary.Load(), raw)
			}
			records, _, err := h.store.ListUsageRecords(context.Background(), store.UsageFilter{})
			if err != nil || len(records) != 1 {
				t.Fatalf("records=%+v err=%v", records, err)
			}
			wantModel := "client-model"
			if tc.summary > 0 {
				wantModel = tc.model
			}
			if records[0].Model != wantModel || records[0].StatusCode != tc.wantStatus {
				t.Errorf("wrong usage model/status: %+v", records[0])
			}
		})
	}
}

func TestConfiguredCompactionModelRestrictions(t *testing.T) {
	for _, mode := range []string{"on", "auto"} {
		t.Run(mode, func(t *testing.T) {
			var summary atomic.Int32
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/compact") {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `{"error":{"message":"This ChatPass credential is not authorized for the requested operation."}}`)
					return
				}
				summary.Add(1)
			}), "sse")
			rt := h.dataPlane.settings.Get()
			rt.CompactionMode = mode
			if err := h.dataPlane.settings.Set(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			a, err := h.store.GetAccount(context.Background(), h.accountID)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.PatchAccountManagementFields(context.Background(), a.ID, store.AccountManagementPatch{DisabledModels: []string{"gpt-6-luna"}}); err != nil {
				t.Fatal(err)
			}
			resp, raw := postCompactTest(t, h, "/v1/responses/compact", `{"model":"client-model","input":[]}`)
			if resp.StatusCode != 403 || !bytes.Contains(raw, []byte("model_disabled")) || summary.Load() != 0 {
				t.Fatalf("restriction bypassed: status=%d summary=%d body=%s", resp.StatusCode, summary.Load(), raw)
			}
			a, err = h.store.GetAccount(context.Background(), h.accountID)
			if err != nil || a.Status != store.AccountStatusReady {
				t.Fatalf("local policy denial changed account status: %+v %v", a, err)
			}
		})
	}
}

func TestAutoSummary429RetryUsesSummaryModel(t *testing.T) {
	var native, summaries atomic.Int32
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/compact") {
			native.Add(1)
			w.WriteHeader(404)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "summary-model" {
			t.Errorf("wrong retry model: %+v", body)
		}
		if summaries.Add(1) == 1 {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"limited"}}`)
			return
		}
		_, _ = io.WriteString(w, summaryTestSSE)
	}), "sse")
	ctx := context.Background()
	a := &store.Account{Name: "second", AccountID: "acct-second", AccessToken: fakeJWT(t, "acct-second"), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Enabled: true, Weight: 1, Concurrency: 3, Status: store.AccountStatusReady}
	if err := h.store.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	rt := h.dataPlane.settings.Get()
	rt.CompactionMode, rt.CompactionModel = "auto", "summary-model"
	rt.SwitchOn429, rt.MaxAttempts = true, 2
	if err := h.dataPlane.settings.Set(ctx, rt); err != nil {
		t.Fatal(err)
	}
	h.dataPlane.accounts.SetSettings(rt)
	resp, raw := postCompactTest(t, h, "/v1/responses/compact", `{"model":"client-model","input":[]}`)
	if resp.StatusCode != 200 || native.Load() != 1 || summaries.Load() != 2 {
		t.Fatalf("status=%d native=%d summaries=%d body=%s", resp.StatusCode, native.Load(), summaries.Load(), raw)
	}
	records, _, err := h.store.ListUsageRecords(ctx, store.UsageFilter{})
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	for _, record := range records {
		if record.Model != "summary-model" {
			t.Errorf("wrong usage model: %+v", record)
		}
	}
}
