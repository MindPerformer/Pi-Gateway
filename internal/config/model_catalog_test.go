package config

import "testing"

func TestModelCatalogURLUsesConfiguredPrimaryEndpoint(t *testing.T) {
	for _, tt := range []struct{ base, want string }{
		{"", "https://api.openai.com/v1/models"},
		{"https://example.test/v1", "https://example.test/v1/models"},
		{"https://example.test/v1/responses", "https://example.test/v1/models"},
		{"http://localhost:18080/custom/responses/?route=a%2Fb", "http://localhost:18080/custom/models?route=a%2Fb"},
		{"https://example.test/custom%2Fbase/", "https://example.test/custom%2Fbase/models"},
	} {
		cfg := Default()
		cfg.Upstream.BaseURL = tt.base
		if got := cfg.UpstreamModelsURL(); got != tt.want {
			t.Errorf("base=%q got=%q want=%q", tt.base, got, tt.want)
		}
	}
}
