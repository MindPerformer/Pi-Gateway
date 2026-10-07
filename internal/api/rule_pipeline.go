package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pi-gateway/internal/piwire"
	"pi-gateway/internal/rules"
	"pi-gateway/internal/store"
)

func (s *Server) ruleSettings(rt *store.Settings) map[string]any {
	mapping := map[string]any{}
	for k, v := range rt.ModelMappings {
		mapping[k] = v
	}
	ua := firstNonEmpty(rt.UserAgent, s.cfg.Upstream.UserAgent, piwire.DefaultUserAgent())
	return map[string]any{"compaction_mode": rt.CompactionMode, "compaction_model": rt.CompactionModel, "default_model": rt.DefaultModel, "model_mappings": mapping, "reasoning_effort": rt.ReasoningEffort, "reasoning_summary": rt.ReasoningSummary, "user_agent": ua, "originator": firstNonEmpty(s.cfg.Upstream.Originator, "pi"), "stainless_os": s.cfg.Upstream.StainlessOS, "stainless_arch": s.cfg.Upstream.StainlessArch, "stainless_runtime": s.cfg.Upstream.StainlessRuntime, "stainless_runtime_version": s.cfg.Upstream.StainlessRuntimeVersion, "timeout_seconds": s.cfg.Upstream.RequestTimeoutSeconds}
}

func (p *prepared) applyPipeline(ctx context.Context, phase string, body any) (rules.Result, error) {
	input := p.ruleInput(body, "")
	if phase == rules.PhaseClientRequest || phase == rules.PhaseRequestNormalize {
		input.Model = ""
	} // Routing is derived after construction.
	if p.Recorder != nil {
		p.Recorder.OnRuleInput(phase, input, false)
	}
	result, err := p.RuleEngine.Apply(ctx, phase, input)
	p.recordRuleResult(result, "", "", nil)
	if err == nil && result.Blocked {
		return result, &apiError{Status: result.Status, Message: result.Reason, Type: "invalid_request_error", Code: "blocked_by_rule"}
	}
	return result, err
}

func (p *prepared) finalizeRules(ctx context.Context) error {
	result, err := p.applyPipeline(ctx, rules.PhaseRequestFinalize, p.Built.Body)
	if err != nil {
		return err
	}
	body, err := ruleOrderedBody(result.Body, p.Built.Body)
	if err != nil {
		return err
	}
	model, exists := body.Get("model")
	if !exists {
		return fmt.Errorf("final request requires model")
	}
	name, ok := model.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("final request requires a nonempty model")
	}
	p.Built.Body, p.Built.Model = body, name
	p.RuleContext["model"] = name
	p.Built.JSON, err = json.Marshal(body)
	return err
}

func (p *prepared) buildRuleHeaders(ctx context.Context, transport, credentialAccount string) (http.Header, error) {
	facts := map[string]any{}
	for k, v := range p.RuleContext {
		facts[k] = v
	}
	facts["transport"] = transport
	facts["credential_account_id"] = credentialAccount
	input := p.ruleInput(map[string]any{}, "")
	input.Context = facts
	if p.Recorder != nil {
		p.Recorder.OnRuleInput(rules.PhaseUpstreamHeaders, input, false)
	}
	result, err := p.RuleEngine.Apply(ctx, rules.PhaseUpstreamHeaders, input)
	p.recordRuleResult(result, "", "", nil)
	if err != nil {
		return nil, err
	}
	return rules.DecodeHeaders(result.Body)
}

func pipelineAPIError(err error) *apiError {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return ruleAPIError(err.Error())
}
