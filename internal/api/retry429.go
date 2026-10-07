package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// switchOn429 applies enabled group overrides within the requesting key's scope.
// Disabled wins when multiple applicable groups disagree.
func (s *Server) switchOn429(ctx context.Context, p *prepared) bool {
	enabled := s.settings.Get().SwitchOn429
	groups, err := s.store.ListAccountGroups(ctx)
	if err != nil {
		return false
	}
	for _, g := range groups {
		if !g.Enabled || !containsID(p.Account.GroupIDs, g.ID) || (len(p.Key.GroupIDs) > 0 && !containsID(p.Key.GroupIDs, g.ID)) {
			continue
		}
		if g.SwitchOn429 == "disabled" {
			return false
		}
		if g.SwitchOn429 == "enabled" {
			enabled = true
		}
	}
	return enabled
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func retryAfterDuration(h http.Header, now time.Time) time.Duration {
	var delay time.Duration
	if n, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil && n > 0 && n <= 604800000 {
		delay = time.Duration(n * float64(time.Millisecond))
	}
	if n, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil && n > 0 && n <= 604800 {
		delay = max(delay, time.Duration(n*float64(time.Second)))
	} else if at, err := http.ParseTime(h.Get("Retry-After")); err == nil {
		delay = max(delay, at.Sub(now))
	}
	return delay
}

// Retry only before any ordinary upstream event is delivered. Continuations
// belong to the original account and socket and cannot be moved safely.
func (s *Server) streamWith429Retry(ctx context.Context, p *prepared, req *upstream.Request, onEvent func(*upstream.Event) error) (*upstream.StreamResult, error) {
	attempts := max(1, s.settings.Get().MaxAttempts)
	excluded := map[int64]bool{}
	for attempt := 1; ; attempt++ {
		maySwitch := attempt < attempts && p.Continuation == nil && p.PreviousResponseID == "" && s.switchOn429(ctx, p)
		delivered := false
		var pending *upstream.Event
		result, err := s.upstream.Stream(ctx, req, func(event *upstream.Event) error {
			if maySwitch && !delivered {
				if failure := upstream.FailureFromEvent(event); failure != nil && errorFromUpstream(failure).Status == 429 {
					pending = event
					return nil
				}
			}
			delivered = true
			return onEvent(event)
		})
		now := time.Now()
		status := 200
		message := ""
		if err != nil {
			status = errorFromUpstream(err).Status
			message = err.Error()
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		healthStatus := status
		var failure *upstream.FailureError
		if errors.As(err, &failure) && failure.OperationDenied {
			healthStatus = 0
		}
		recordErr := s.accounts.RecordResult(recordCtx, p.Account.ID, accounts.Result{Failed: err != nil, StatusCode: healthStatus, RetryAfter: retryAfterDuration(p.ResponseHeaders, now), Error: message}, now)
		cancel()
		if recordErr != nil {
			s.logger.Warn("recording account attempt failed", "account", p.Account.ID, "error", recordErr)
		}
		if err == nil || status != 429 || !maySwitch || delivered || ctx.Err() != nil {
			if pending != nil {
				if deliveryErr := onEvent(pending); deliveryErr != nil {
					return result, deliveryErr
				}
			}
			return result, err
		}
		excluded[p.Account.ID] = true
		if p.AccountRelease != nil {
			p.AccountRelease()
		}
		next, release, acquireErr := s.accounts.AcquireExcluding(ctx, p.Key, p.Built.Model, "", excluded)
		if acquireErr != nil {
			if pending != nil {
				if deliveryErr := onEvent(pending); deliveryErr != nil {
					return result, deliveryErr
				}
			}
			return result, err
		}
		// Preserve a separate usage entry for the failed attempt before moving on.
		if p.Usage != nil {
			p.Usage.finalize(context.WithoutCancel(ctx), store.OutcomeError, message)
		}
		_ = s.store.MarkAccountUsed(context.WithoutCancel(ctx), p.Account.ID, true)
		p.Account, p.AccountRelease = next, release
		p.Release = composeRelease(p.Release, release)
		clientTransport := "sse"
		if p.Usage != nil {
			clientTransport = p.Usage.clientTransport
		}
		p.Transport = s.resolveTransport(s.settings.Get(), p.Key, next, clientTransport)
		if p.Compact {
			p.Transport = "sse"
		}
		p.ResponseHeaders = nil
		p.RuleContext["account_id"], p.RuleContext["upstream_protocol"] = next.ID, p.Transport
		if p.Recorder != nil {
			p.Recorder.SetRoute(next.ID, next.Name, p.Built.Model, p.SessionID, p.Transport)
		}
		p.Usage = s.newUsageTracker(p.Key, next, p.Built.Model, clientTransport, p.Transport, p.SessionID, stringField(p.ClientBody, "service_tier"))
		p.Usage.noteRequestMetadata(p.Built.JSON)
		if p.Compact {
			p.Usage.requestKind = "compaction"
		}
		s.startUsage(ctx, p.Usage)
		req, err = s.newUpstreamRequest(ctx, p)
		if err != nil {
			return nil, err
		}
	}
}
