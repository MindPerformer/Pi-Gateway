package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pi-gateway/internal/store"
)

// Time parameters are Unix milliseconds, matching usage_records. Omitting them
// means "the last 24 hours", which is what the statistics page opens with.
const defaultUsageWindow = 24 * time.Hour

const (
	usageDefaultLimit = 50
	usageMaxLimit     = 200
)

// usageFilterFromQuery parses the shared range/filter query parameters.
func usageFilterFromQuery(r *http.Request) (store.UsageFilter, error) {
	q := r.URL.Query()
	f := store.UsageFilter{}

	start, err := queryInt64(q.Get("start"))
	if err != nil {
		return f, errInvalid("start must be a unix millisecond timestamp")
	}
	end, err := queryInt64(q.Get("end"))
	if err != nil {
		return f, errInvalid("end must be a unix millisecond timestamp")
	}
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	if start == 0 {
		start = end - defaultUsageWindow.Milliseconds()
	}
	if start >= end {
		return f, errInvalid("start must be earlier than end")
	}
	f.Start, f.End = start, end

	if v := strings.TrimSpace(q.Get("api_key_id")); v != "" {
		id, err := queryInt64(v)
		if err != nil || id < 0 {
			return f, errInvalid("api_key_id must be a positive integer")
		}
		f.APIKeyID = id
	}
	if v := strings.TrimSpace(q.Get("account_id")); v != "" {
		id, err := queryInt64(v)
		if err != nil || id < 0 {
			return f, errInvalid("account_id must be a positive integer")
		}
		f.AccountID = id
	}
	f.Model = strings.TrimSpace(q.Get("model"))
	f.Outcome = strings.TrimSpace(q.Get("outcome"))
	f.Search = strings.TrimSpace(q.Get("search"))
	return f, nil
}

func queryInt64(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

func costUSD(micros int64) float64 { return float64(micros) / 1e6 }

func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilterFromQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sum, err := s.store.UsageSummaryFor(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rate := 0.0
	if sum.InputTokens > 0 {
		rate = float64(sum.CachedTokens) / float64(sum.InputTokens)
	}
	hitRate := 0.0
	if sum.CacheEligibleRequests > 0 {
		hitRate = float64(sum.CacheHitRequests) / float64(sum.CacheEligibleRequests)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"start":                   f.Start,
		"end":                     f.End,
		"request_count":           sum.RequestCount,
		"success_count":           sum.SuccessCount,
		"failure_count":           sum.FailureCount,
		"cancelled_count":         sum.CancelledCount,
		"incomplete_count":        sum.IncompleteCount,
		"input_tokens":            sum.InputTokens,
		"output_tokens":           sum.OutputTokens,
		"cached_tokens":           sum.CachedTokens,
		"cache_write_tokens":      sum.CacheWriteTokens,
		"reasoning_tokens":        sum.ReasoningTokens,
		"total_tokens":            sum.TotalTokens,
		"cache_eligible_requests": sum.CacheEligibleRequests,
		"cache_hit_requests":      sum.CacheHitRequests,
		"cached_token_rate":       rate,
		"cache_hit_request_rate":  hitRate,
		"avg_latency_ms":          sum.AvgLatencyMS,
		"ttft_p50_ms":             sum.TTFT.P50,
		"ttft_p90_ms":             sum.TTFT.P90,
		"ttft_p95_ms":             sum.TTFT.P95,
		"output_tps_p50":          sum.OutputTPS.P50,
		"output_tps_p90":          sum.OutputTPS.P90,
		"total_cost_usd":          costUSD(sum.CostMicros),
		"total_cost_micros":       sum.CostMicros,
	})
}

func (s *Server) handleStatsTrend(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilterFromQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	bucket := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("bucket")))
	if bucket == "" {
		bucket = "hour"
	}
	if bucket != "hour" && bucket != "day" {
		writeErr(w, http.StatusBadRequest, "bucket must be hour or day")
		return
	}
	buckets, err := s.store.UsageTrend(r.Context(), f, bucket)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, map[string]any{
			"bucket_start":   b.BucketStart,
			"request_count":  b.RequestCount,
			"input_tokens":   b.InputTokens,
			"output_tokens":  b.OutputTokens,
			"cached_tokens":  b.CachedTokens,
			"total_tokens":   b.TotalTokens,
			"avg_latency_ms": b.AvgLatencyMS,
			"ttft_ms":        b.TTFTMS,
			"output_tps":     b.OutputTPS,
			"cost_usd":       costUSD(b.CostMicros),
			"cost_micros":    b.CostMicros,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": bucket, "buckets": out})
}

func (s *Server) handleStatsModels(w http.ResponseWriter, r *http.Request) {
	s.writeDimensions(w, r, "models")
}

func (s *Server) handleStatsKeys(w http.ResponseWriter, r *http.Request) {
	s.writeDimensions(w, r, "keys")
}

func (s *Server) handleStatsAccounts(w http.ResponseWriter, r *http.Request) {
	s.writeDimensions(w, r, "accounts")
}

func (s *Server) writeDimensions(w http.ResponseWriter, r *http.Request, kind string) {
	f, err := usageFilterFromQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var dims []store.UsageDimension
	switch kind {
	case "models":
		dims, err = s.store.UsageByModel(r.Context(), f)
	case "keys":
		dims, err = s.store.UsageByKey(r.Context(), f)
	default:
		dims, err = s.store.UsageByAccount(r.Context(), f)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(dims))
	for _, d := range dims {
		items = append(items, map[string]any{
			"key":            d.Key,
			"id":             d.ID,
			"request_count":  d.RequestCount,
			"success_count":  d.SuccessCount,
			"failure_count":  d.FailureCount,
			"input_tokens":   d.InputTokens,
			"output_tokens":  d.OutputTokens,
			"cached_tokens":  d.CachedTokens,
			"total_tokens":   d.TotalTokens,
			"avg_latency_ms": d.AvgLatencyMS,
			"ttft_ms":        d.TTFTMS,
			"output_tps":     d.OutputTPS,
			"cost_usd":       costUSD(d.CostMicros),
			"cost_micros":    d.CostMicros,
			"share":          d.Share,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleUsageRecords serves the paginated ledger. Nullable token fields stay
// null in the response so the UI can render "unknown" instead of zero.
func (s *Server) handleUsageRecords(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilterFromQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit := usageDefaultLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	if limit <= 0 || limit > usageMaxLimit {
		limit = usageDefaultLimit
	}
	offset := 0
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}
	f.Limit, f.Offset = limit, offset

	records, total, err := s.store.ListUsageRecords(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		var billingDetails any
		if rec.BillingDetails != "" {
			_ = json.Unmarshal([]byte(rec.BillingDetails), &billingDetails)
		}
		var costUSDValue any
		if rec.CostMicros != nil {
			costUSDValue = costUSD(*rec.CostMicros)
		}
		items = append(items, map[string]any{
			"id":                     rec.ID,
			"request_id":             rec.RequestID,
			"api_key_id":             rec.APIKeyID,
			"api_key_name":           rec.APIKeyName,
			"account_id":             rec.AccountID,
			"account_name":           rec.AccountName,
			"model":                  rec.Model,
			"reasoning_effort":       rec.ReasoningEffort,
			"requested_service_tier": rec.RequestedServiceTier,
			"service_tier":           rec.ServiceTier,
			"service_priority":       rec.ServiceTier,
			"billing_details":        billingDetails,
			"price_version":          rec.PriceVersion,
			"request_kind":           rec.RequestKind,
			"client_transport":       rec.ClientTransport,
			"upstream_transport":     rec.UpstreamTransport,
			"outcome":                rec.Outcome,
			"status_code":            rec.StatusCode,
			"error_code":             rec.ErrorCode,
			"error_message":          rec.ErrorMessage,
			"input_tokens":           rec.InputTokens,
			"cached_tokens":          rec.CachedTokens,
			"cache_write_tokens":     rec.CacheWriteTokens,
			"output_tokens":          rec.OutputTokens,
			"reasoning_tokens":       rec.ReasoningTokens,
			"total_tokens":           rec.TotalTokens,
			"first_token_ms":         rec.FirstTokenMS,
			"latency_ms":             rec.LatencyMS,
			"output_tps":             rec.OutputTPS(),
			"cost_usd":               costUSDValue,
			"cost_micros":            rec.CostMicros,
			"cost_source":            rec.CostSource,
			"session_id":             rec.SessionID,
			"started_at":             rec.StartedAt,
			"completed_at":           rec.CompletedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": items, "total": total, "limit": limit, "offset": offset})
}
