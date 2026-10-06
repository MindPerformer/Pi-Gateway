package piwire

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestBuildRequestMatchesPiFieldOrder locks in the exact key order Pi's
// openai-responses (Sign in with ChatGPT) route produces, because the upstream
// sees these bytes.
func TestBuildRequestMatchesPiFieldOrder(t *testing.T) {
	client := map[string]any{
		"model": "gpt-5.1-codex",
		"input": []any{map[string]any{"type": "message", "role": "user"}},
	}
	built, err := BuildRequest(client, BuildOptions{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	want := `{"model":"gpt-5.1-codex","input":[{"role":"user","type":"message"}],` +
		`"stream":true,"prompt_cache_key":"sess-1","store":false}`
	if string(built.JSON) != want {
		t.Errorf("request body mismatch\n got: %s\nwant: %s", built.JSON, want)
	}
}

// The preview's explicit field restrictions apply even with ExtraFields enabled.
func TestBuildRequestDropsRejectedFields(t *testing.T) {
	rejected := []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "type"}
	client := map[string]any{
		"model": "gpt-5.1-codex", "input": []any{},
	}
	for _, field := range rejected {
		client[field] = "rejected"
	}
	built, err := BuildRequest(client, BuildOptions{ExtraFields: rejected})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	for _, forbidden := range rejected {
		if built.Body.Has(forbidden) {
			t.Errorf("field %s should not be forwarded: %s", forbidden, built.JSON)
		}
		built.Body.Set(forbidden, "reintroduced by rule")
	}
	EnforcePiShape(built.Body)
	for _, forbidden := range rejected {
		if built.Body.Has(forbidden) {
			t.Errorf("rule reintroduced forbidden field %s", forbidden)
		}
	}
}

// TestBuildRequestAppendsPreviousResponseIDLast mirrors Pi's spread order, where
// previous_response_id is appended after the copied keys.
func TestBuildRequestAppendsPreviousResponseIDLast(t *testing.T) {
	client := map[string]any{
		"model":                "gpt-5-codex",
		"input":                []any{},
		"previous_response_id": "resp_prev",
		"reasoning":            map[string]any{"effort": "high"},
	}
	built, err := BuildRequest(client, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	got := string(built.JSON)
	if !strings.HasSuffix(got, `,"previous_response_id":"resp_prev"}`) {
		t.Errorf("previous_response_id is not last: %s", got)
	}
	if !strings.Contains(got, `"reasoning":{"effort":"high"}`) {
		t.Errorf("reasoning not rendered from the client value: %s", got)
	}
}

func TestBuildRequestPreservesClientOptions(t *testing.T) {
	client := map[string]any{
		"model": "gpt-5-codex", "input": []any{},
		"instructions":         "be terse",
		"text":                 map[string]any{"verbosity": "high", "format": map[string]any{"type": "json_object"}},
		"parallel_tool_calls":  false,
		"prompt_cache_options": map[string]any{"mode": "explicit"},
		"future_option":        map[string]any{"empty": []any{}, "null": nil, "flag": false},
		"nullable_option":      nil,
		"reasoning":            map[string]any{"effort": "high", "future_option": false, "summary": nil},
		"previous_response_id": "resp-prev",
	}
	built, err := BuildRequest(client, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	EnforcePiShape(built.Body)
	for _, field := range []string{"instructions", "text", "parallel_tool_calls", "prompt_cache_options", "future_option", "nullable_option", "reasoning"} {
		if got, exists := built.Body.Get(field); !exists || !reflect.DeepEqual(got, client[field]) {
			t.Errorf("field %s changed: got %#v want %#v", field, got, client[field])
		}
	}
	keys := built.Body.Keys()
	if keys[len(keys)-1] != "previous_response_id" {
		t.Errorf("extra fields displaced continuation: %v", keys)
	}
}

// TestBuildRequestAppliesModelMapping verifies model rewriting.
func TestBuildRequestAppliesModelMapping(t *testing.T) {
	built, err := BuildRequest(map[string]any{"model": "gpt-4o", "input": []any{}},
		BuildOptions{ModelMappings: map[string]string{"gpt-4o": "gpt-5.1-codex"}})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if built.Model != "gpt-5.1-codex" {
		t.Errorf("model = %q, want the mapped value", built.Model)
	}
}

// TestBuildRequestUsesDefaultModel verifies the fallback.
func TestBuildRequestUsesDefaultModel(t *testing.T) {
	built, err := BuildRequest(map[string]any{"input": []any{}},
		BuildOptions{DefaultModel: "gpt-5.1-codex"})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if built.Model != "gpt-5.1-codex" {
		t.Errorf("model = %q, want the default", built.Model)
	}

	if _, err := BuildRequest(map[string]any{}, BuildOptions{}); err == nil {
		t.Error("expected an error when neither a model nor a default is available")
	}
}

// TestClampPromptCacheKey verifies Pi's 64-character clamp.
func TestClampPromptCacheKey(t *testing.T) {
	short := "abc"
	if got := ClampPromptCacheKey(short); got != short {
		t.Errorf("short key changed: %q", got)
	}

	long := strings.Repeat("x", 100)
	got := ClampPromptCacheKey(long)
	if len([]rune(got)) != PromptCacheKeyMaxLength {
		t.Errorf("clamped length = %d, want %d", len([]rune(got)), PromptCacheKeyMaxLength)
	}

	// Multi-byte characters must be counted as code points, like Array.from in JS.
	unicode := strings.Repeat("日", 100)
	gotUnicode := ClampPromptCacheKey(unicode)
	if len([]rune(gotUnicode)) != PromptCacheKeyMaxLength {
		t.Errorf("clamped unicode length = %d runes, want %d", len([]rune(gotUnicode)), PromptCacheKeyMaxLength)
	}
}

// TestSSEHeadersMatchPi verifies the header set for the Sign in with ChatGPT HTTP
// route: no Codex-only originator/chatgpt-account-id/OpenAI-Beta headers.
func TestSSEHeadersMatchPi(t *testing.T) {
	h := BuildSSEHeaders(HeaderOptions{
		AccessToken: "tok-123",
		AccountID:   "acct-1",
		SessionID:   "sess-1",
		Originator:  "pi",
		UserAgent:   "pi (linux 6.1.0; x64)",
		Platform: Platform{
			OS: "Linux", Arch: "x64", Runtime: "node", RuntimeVersion: "v22.0.0",
		},
	})

	expected := map[string]string{
		"Authorization":               "Bearer tok-123",
		"User-Agent":                  "pi (linux 6.1.0; x64)",
		"accept":                      "application/json",
		"content-type":                "application/json",
		"session_id":                  "sess-1",
		"x-client-request-id":         "sess-1",
		"accept-encoding":             "gzip, deflate",
		"X-Stainless-Lang":            "js",
		"X-Stainless-Package-Version": "7.19.0",
		"X-Stainless-OS":              "Linux",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v22.0.0",
		"X-Stainless-Retry-Count":     "0",
	}
	for name, want := range expected {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, forbidden := range []string{
		"chatgpt-account-id", "originator", "OpenAI-Beta", "X-Api-Key",
		"X-Stainless-Helper-Method", "X-Stainless-Poll-Helper", "X-Stainless-Timeout",
	} {
		if got, present := h[http.CanonicalHeaderKey(forbidden)]; present {
			t.Errorf("%s = %q, want it absent on the ChatGPT route", forbidden, got)
		}
	}
	if h.Get("content-encoding") != "" {
		t.Error("content-encoding must only be set when the body is compressed")
	}
}

func TestSSEHeadersIncludeExplicitTimeout(t *testing.T) {
	h := BuildSSEHeaders(HeaderOptions{TimeoutSeconds: 600})
	if got := h.Get("X-Stainless-Timeout"); got != "600" {
		t.Errorf("X-Stainless-Timeout = %q, want 600", got)
	}
}

func TestHeadersOmitSessionHeadersWithoutSessionID(t *testing.T) {
	for transport, build := range map[string]func(HeaderOptions) http.Header{
		"SSE": BuildSSEHeaders,
		"WS":  BuildWSHeaders,
	} {
		t.Run(transport, func(t *testing.T) {
			h := build(HeaderOptions{})
			for _, name := range []string{"session_id", "session-id", "x-client-request-id"} {
				if got, present := h[http.CanonicalHeaderKey(name)]; present {
					t.Errorf("%s = %q, want it absent without a wire session id", name, got)
				}
			}
		})
	}
}

func TestSSEHeadersOmitNonPositiveTimeout(t *testing.T) {
	for _, seconds := range []int{0, -1} {
		h := BuildSSEHeaders(HeaderOptions{TimeoutSeconds: seconds})
		if got, present := h[http.CanonicalHeaderKey("X-Stainless-Timeout")]; present {
			t.Errorf("timeout %d: X-Stainless-Timeout = %q, want it absent", seconds, got)
		}
	}
}

func TestSSEHeadersOmitEmptyPlatformFields(t *testing.T) {
	for _, platform := range []Platform{
		{},
		{OS: "custom-os"},
		{Arch: "custom-arch"},
		{Runtime: "custom-runtime"},
		{RuntimeVersion: "custom-version"},
	} {
		h := BuildSSEHeaders(HeaderOptions{Platform: platform})
		for name, want := range map[string]string{
			"X-Stainless-OS":              platform.OS,
			"X-Stainless-Arch":            platform.Arch,
			"X-Stainless-Runtime":         platform.Runtime,
			"X-Stainless-Runtime-Version": platform.RuntimeVersion,
		} {
			if want == "" {
				if got, present := h[http.CanonicalHeaderKey(name)]; present {
					t.Errorf("platform %+v: %s = %q, want it absent", platform, name, got)
				}
			} else if got := h.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
	}
}

// TestWSHeadersMatchPi verifies the WebSocket handshake header set, which drops
// accept/content-type and advertises the responses websockets beta.
func TestWSHeadersMatchPi(t *testing.T) {
	h := BuildWSHeaders(HeaderOptions{
		AccessToken:    "tok-123",
		AccountID:      "acct-1",
		SessionID:      "sess-1",
		TimeoutSeconds: 600,
		Platform: Platform{
			OS: "Linux", Arch: "x64", Runtime: "node", RuntimeVersion: "v22.0.0",
		},
	})

	if got := h.Get("OpenAI-Beta"); got != BetaWebSocket {
		t.Errorf("OpenAI-Beta = %q, want %q", got, BetaWebSocket)
	}
	if got := h.Get("originator"); got != DefaultOriginator {
		t.Errorf("originator = %q, want %q", got, DefaultOriginator)
	}
	for _, name := range []string{
		"Accept", "Content-Type",
		"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS",
		"X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count", "X-Stainless-Timeout",
	} {
		if got, present := h[http.CanonicalHeaderKey(name)]; present {
			t.Errorf("%s = %q, must not be sent on the websocket handshake", name, got)
		}
	}
	if got := h.Get("session-id"); got != "sess-1" {
		t.Errorf("session-id = %q", got)
	}
	if got := h.Get("x-client-request-id"); got != "sess-1" {
		t.Errorf("x-client-request-id = %q", got)
	}
	if got := h.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "pi (") {
		t.Errorf("User-Agent = %q, want a pi (...) default", got)
	}
}

func TestWSHeadersPreserveExplicitOriginator(t *testing.T) {
	h := BuildWSHeaders(HeaderOptions{Originator: "custom-pi"})
	if got := h.Get("originator"); got != "custom-pi" {
		t.Errorf("originator = %q, want custom-pi", got)
	}
}

// TestWSHeadersOmitAccountIDWhenAbsent verifies the ChatGPT credential (which has
// no chatgpt_account_id claim) does not send an empty account header.
func TestWSHeadersOmitAccountIDWhenAbsent(t *testing.T) {
	h := BuildWSHeaders(HeaderOptions{AccessToken: "tok", SessionID: "s"})
	if got := h.Get("chatgpt-account-id"); got != "" {
		t.Errorf("chatgpt-account-id = %q, want it absent", got)
	}
}

// TestDeriveSessionIDPreferenceOrder verifies session id resolution.
func TestDeriveSessionIDPreferenceOrder(t *testing.T) {
	// The body's prompt_cache_key wins.
	got := DeriveSessionID(map[string]any{"prompt_cache_key": "from-body"}, []string{"from-header"}, nil)
	if got != "from-body" {
		t.Errorf("session id = %q, want from-body", got)
	}

	// Otherwise the first non-empty header hint is used.
	got = DeriveSessionID(map[string]any{}, []string{"", "hint"}, func() string { return "generated" })
	if got != "hint" {
		t.Errorf("session id = %q, want hint", got)
	}

	// Otherwise the generator runs.
	got = DeriveSessionID(map[string]any{}, nil, func() string { return "generated" })
	if got != "generated" {
		t.Errorf("session id = %q, want generated", got)
	}

	// The body value is clamped like Pi clamps prompt_cache_key.
	long := strings.Repeat("y", 200)
	got = DeriveSessionID(map[string]any{"prompt_cache_key": long}, nil, nil)
	if len([]rune(got)) != PromptCacheKeyMaxLength {
		t.Errorf("clamped session id length = %d", len([]rune(got)))
	}
}

// TestOrderedMapPreservesInsertionOrder verifies the JSON key ordering helper.
func TestOrderedMapPreservesInsertionOrder(t *testing.T) {
	m := NewOrderedMap()
	m.Set("z", 1).Set("a", 2).Set("m", 3)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"z":1,"a":2,"m":3}` {
		t.Errorf("order not preserved: %s", raw)
	}

	// Re-setting an existing key keeps its position (JavaScript semantics).
	m.Set("z", 9)
	raw, _ = json.Marshal(m)
	if string(raw) != `{"z":9,"a":2,"m":3}` {
		t.Errorf("re-set moved the key: %s", raw)
	}

	// SetLast moves a key to the end, which is how previous_response_id lands last.
	m.SetLast("a", 5)
	raw, _ = json.Marshal(m)
	if string(raw) != `{"z":9,"m":3,"a":5}` {
		t.Errorf("SetLast did not append: %s", raw)
	}
}

// TestDefaultUserAgentDoesNotLeakHost verifies the default UA is a plausible Pi
// value rather than the real build platform.
func TestDefaultUserAgentDoesNotLeakHost(t *testing.T) {
	ua := DefaultUserAgent()
	if !strings.HasPrefix(ua, "pi (") || !strings.HasSuffix(ua, ")") {
		t.Errorf("User-Agent = %q, want the pi (<platform> <release>; <arch>) shape", ua)
	}
	if !strings.Contains(ua, "linux") || !strings.Contains(ua, "x64") {
		t.Errorf("User-Agent = %q, want a plausible linux/x64 value", ua)
	}
}

// TestBuildRequestOmitsPromptCacheKeyWithoutSession locks in Pi's rule: the
// prompt_cache_key field is only sent when a session id actually exists, so a
// generated id can never leak into the upstream body.
func TestBuildRequestOmitsPromptCacheKeyWithoutSession(t *testing.T) {
	built, err := BuildRequest(map[string]any{
		"model": "gpt-5.1-codex",
		"input": []any{},
	}, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if strings.Contains(string(built.JSON), "prompt_cache_key") {
		t.Errorf("prompt_cache_key must be omitted without a session: %s", built.JSON)
	}
	want := `{"model":"gpt-5.1-codex","input":[],"stream":true,"store":false}`
	if string(built.JSON) != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", built.JSON, want)
	}
}

// TestEnforcePiShapeRepairsMiddlewareDrift verifies the route invariants are
// re-asserted after the middleware chain: rejected fields are dropped, store/stream
// are forced, and previous_response_id returns to the end.
func TestEnforcePiShapeRepairsMiddlewareDrift(t *testing.T) {
	body := NewOrderedMap()
	body.Set("model", "gpt-5.1-codex")
	body.Set("store", true)
	body.Set("temperature", 0.7)
	body.Set("max_output_tokens", 512)
	body.Set("previous_response_id", "resp-1")
	body.Set("input", []any{})

	changes := EnforcePiShape(body)
	if len(changes) == 0 {
		t.Fatalf("expected reported changes")
	}
	if body.Has("temperature") || body.Has("max_output_tokens") {
		t.Errorf("rejected fields survived: %v", body.Keys())
	}
	store, _ := body.Get("store")
	if store != false {
		t.Errorf("store = %v, want false", store)
	}
	stream, _ := body.Get("stream")
	if stream != true {
		t.Errorf("stream = %v, want true", stream)
	}
	keys := body.Keys()
	if keys[len(keys)-1] != "previous_response_id" {
		t.Errorf("previous_response_id is not last: %v", keys)
	}
}

// TestEnforcePiShapeLeavesLegitimateFieldsAlone verifies genuine Responses API
// fields, which are not part of Pi's own payload but are still valid, survive.
func TestEnforcePiShapeLeavesLegitimateFieldsAlone(t *testing.T) {
	body := NewOrderedMap()
	body.Set("model", "gpt-5.1-codex")
	body.Set("instructions", "preserve this")
	body.Set("text", map[string]any{"verbosity": "high"})
	body.Set("parallel_tool_calls", false)

	EnforcePiShape(body)

	if !body.Has("instructions") || !body.Has("text") || !body.Has("parallel_tool_calls") {
		t.Errorf("legitimate fields were dropped: %v", body.Keys())
	}
}
func TestBuildRequestPreservesExplicitPreviousResponseID(t *testing.T) {
	built, err := BuildRequest(map[string]any{
		"model":                "gpt-5.1-codex",
		"input":                []any{},
		"previous_response_id": "resp-42",
	}, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !strings.HasSuffix(string(built.JSON), `"previous_response_id":"resp-42"}`) {
		t.Errorf("previous_response_id must be forwarded last: %s", built.JSON)
	}
}

func TestBuildRequestNormalizesStringInput(t *testing.T) {
	for _, text := range []string{"Hi", "你好\nIt's a test", ""} {
		t.Run(text, func(t *testing.T) {
			client := map[string]any{"model": "test", "input": text}
			built, err := BuildRequest(client, BuildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Input []struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(built.JSON, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Input) != 1 || payload.Input[0].Role != "user" || len(payload.Input[0].Content) != 1 || payload.Input[0].Content[0].Type != "input_text" || payload.Input[0].Content[0].Text != text {
				t.Fatalf("unexpected input normalization: %s", built.JSON)
			}
			if client["input"] != text {
				t.Fatal("normalization mutated client input")
			}
		})
	}
}
