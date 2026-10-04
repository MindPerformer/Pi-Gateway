package capture

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"pi-gateway/internal/store"
)

func TestRecorderClientResponseHeaders(t *testing.T) {
	const secret = "abcdefghijklmnopqrstuvwxyz-0123456789"
	headers := http.Header{
		"Content-Type":        {"application/json"},
		"Authorization":       {"Bearer " + secret},
		"Proxy-Authorization": {"Basic " + secret},
		"X-Api-Key":           {secret},
		"Api-Key":             {secret},
		"Openai-Api-Key":      {secret},
		"Cookie":              {"session=" + secret},
		"Set-Cookie":          {"session=" + secret, "other=" + secret},
		"X-Request-Id":        {"req-client"},
	}
	original := headers.Clone()
	rec := New(nil, Options{IncludeHeaders: true})
	rec.OnResponseHeaders(http.StatusForbidden, http.Header{"X-Upstream": {"only"}})
	rec.OnClientResponseHeaders(http.StatusBadGateway, headers)
	headers.Set("X-Request-Id", "changed-after-commit")

	got := rec.Finalize(store.OutcomeError, "")
	if len(got.ResponseFrames) != 1 {
		t.Fatalf("frames = %d, want 1", len(got.ResponseFrames))
	}
	frame := got.ResponseFrames[0]
	if frame.Dir != DirClientOut || frame.Kind != store.KindHandshakeResponse || frame.Seq != 0 {
		t.Fatalf("unexpected header frame: %+v", frame)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok || data["status"] != http.StatusBadGateway {
		t.Fatalf("client response status missing: %#v", frame.Data)
	}
	if !reflect.DeepEqual(data["headers"], flattenHeaders(original)) {
		t.Fatalf("client response headers were not sanitized/snapshotted: %#v", data["headers"])
	}
	if got.Status != http.StatusForbidden || got.ResponseText != "" {
		t.Fatalf("client headers changed upstream status or SSE text: %+v", got)
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("client headers leaked credentials: %s", encoded)
	}
	if got.Truncated {
		t.Fatal("small header frame unexpectedly truncated")
	}
}

func TestRecorderClientResponseHeadersDisabled(t *testing.T) {
	rec := New(nil, Options{IncludeHeaders: false, MaxBytesPerRecord: 1})
	rec.OnClientResponseHeaders(http.StatusForbidden, http.Header{"X-Secret": {strings.Repeat("private", 1000)}})
	got := rec.Finalize(store.OutcomeError, "")
	if len(got.ResponseFrames) != 1 {
		t.Fatalf("frames = %d, want status-only frame", len(got.ResponseFrames))
	}
	data, ok := got.ResponseFrames[0].Data.(map[string]any)
	if !ok || len(data) != 1 || data["status"] != http.StatusForbidden {
		t.Fatalf("headers must be omitted, not empty or null: %#v", got.ResponseFrames[0].Data)
	}
	if rec.seenBytes != 0 || got.Truncated || got.ResponseText != "" {
		t.Fatalf("disabled headers consumed budget or changed response text: %+v", got)
	}
}

func TestRecorderClientResponseHeadersShareBudget(t *testing.T) {
	for _, budget := range []int64{1, 6, 12, 24, 64} {
		rec := New(nil, Options{IncludeHeaders: true, MaxBytesPerRecord: budget})
		rec.OnRequestBody([]byte("req"), false)
		rec.OnClientResponseHeaders(http.StatusOK, http.Header{"X-Long": {strings.Repeat("x", 128)}})
		rec.OnFrame(DirClientOut, store.KindHTTPResponse, "response", []byte(`{"ok":true}`), nil)
		got := rec.Finalize(store.OutcomeOK, "")
		if !got.Truncated || rec.seenBytes > budget || len(got.ResponseFrames) != 2 {
			t.Fatalf("budget %d not enforced: seen=%d capture=%+v", budget, rec.seenBytes, got)
		}
		data := got.ResponseFrames[0].Data.(map[string]any)
		if data["status"] != http.StatusOK {
			t.Fatalf("budget %d lost status metadata: %#v", budget, data)
		}
		total := len(got.RequestBody)
		for _, header := range data["headers"].([]store.Header) {
			total += len(header.Name) + len(header.Value)
		}
		total += len(got.ResponseFrames[1].Type) + len(frameDataString(got.ResponseFrames[1]))
		if int64(total) > budget {
			t.Fatalf("budget %d retained %d payload bytes", budget, total)
		}
	}
}

func TestRecorderClientResponseHeadersFrameCapAndExactBudget(t *testing.T) {
	rec := New(nil, Options{IncludeHeaders: true, MaxBytesPerRecord: 4})
	rec.maxFrames = 1
	rec.OnClientResponseHeaders(http.StatusOK, http.Header{"X": {"abc"}})
	if rec.seenBytes != 4 || rec.truncated {
		t.Fatalf("exact header budget charged %d bytes, truncated=%t", rec.seenBytes, rec.truncated)
	}
	rec.OnClientResponseHeaders(http.StatusForbidden, http.Header{"X": {"must-not-be-processed"}})
	got := rec.Finalize(store.OutcomeOK, "")
	if len(got.ResponseFrames) != 1 || rec.seq != 1 || !got.Truncated || rec.seenBytes != 4 {
		t.Fatalf("frame cap was not applied before processing headers: %+v", got)
	}
}
