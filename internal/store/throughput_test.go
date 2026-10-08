package store

import "testing"

func TestOutputTPSCodex2APIBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		first, latency int64
		outcome        string
		status         int
		output         *int64
		want           *float64
	}{
		{"normal", 200, 1000, "succeeded", 200, usageInt(400), floatPtr(500)},
		{"19ms uses total", 981, 1000, "succeeded", 200, usageInt(400), floatPtr(400)},
		{"20ms uses generation", 980, 1000, "succeeded", 200, usageInt(400), floatPtr(20000)},
		{"missing first", 0, 1000, "succeeded", 200, usageInt(400), floatPtr(400)},
		{"first at completion", 1000, 1000, "succeeded", 200, usageInt(400), floatPtr(400)},
		{"first past completion", 1001, 1000, "succeeded", 200, usageInt(400), floatPtr(400)},
		{"failed", 200, 1000, "failed", 200, usageInt(400), nil},
		{"http failure", 200, 1000, "succeeded", 429, usageInt(400), nil},
		{"cancelled", 200, 1000, "cancelled", 200, usageInt(400), nil},
		{"incomplete", 200, 1000, "incomplete", 200, usageInt(400), nil},
		{"empty output", 200, 1000, "succeeded", 200, usageInt(0), nil},
		{"missing output", 200, 1000, "succeeded", 200, nil, nil},
		{"no duration", 0, 0, "succeeded", 200, usageInt(400), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			r := UsageRecord{Outcome: tc.outcome, StatusCode: tc.status, OutputTokens: tc.output, FirstTokenMS: tc.first, LatencyMS: tc.latency, StartedAt: 1000}
			got := r.OutputTPS()
			if tc.want == nil {
				if got != nil {
					t.Fatalf("TPS=%v", *got)
				}
			} else {
				if got == nil {
					t.Fatal("missing TPS")
				}
				checkFloat(t, "record", *got, *tc.want)
			}
			finishTestUsage(t, s, r)
			sum, err := s.UsageSummaryFor(t.Context(), UsageFilter{})
			if err != nil {
				t.Fatal(err)
			}
			want := 0.0
			if tc.want != nil {
				want = *tc.want
			}
			checkFloat(t, "summary", sum.OutputTPS.P50, want)
			trend, err := s.UsageTrend(t.Context(), UsageFilter{}, "day")
			if err != nil {
				t.Fatal(err)
			}
			checkFloat(t, "trend", trend[0].OutputTPS, want)
			models, err := s.UsageByModel(t.Context(), UsageFilter{})
			if err != nil {
				t.Fatal(err)
			}
			checkFloat(t, "dimension", models[0].OutputTPS, want)
		})
	}
}

func floatPtr(n float64) *float64 { return &n }
