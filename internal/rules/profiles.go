package rules

// DefaultProfile is ordinary editable rule data. Field names and transport
// fingerprints live here, outside the generic interpreter.
func DefaultProfile() []Rule {
	base := func(id, name, phase string, actions []Action) Rule {
		return Rule{SchemaVersion: SchemaVersion, ID: id, Name: name, Description: "协议默认规则；每一步均可展开编辑。", Enabled: true, Priority: -1000, Phase: phase, When: Condition{Op: "always"}, Actions: actions, OnError: OnErrorAbort, Source: "protocol"}
	}
	client := func(path string) any { return refExpr("original", path) }
	fact := func(path string) any { return refExpr("context", path) }
	normalize := []Action{
		setStep("new-request", "", map[string]any{}),
		setStep("model", "/model", expr("coalesce", client("/model"), fact("/settings/default_model"))),
		branch("default-empty-model", cond("eq", "/model", ""), []Action{setStep("default-model", "/model", fact("/settings/default_model"))}),
		step("remember-model", "let", map[string]any{"name": "model", "value": refExpr("current", "/model")}),
		setStep("model-mapping", "/model", expr("coalesce", expr("get", fact("/settings/model_mappings"), expr("concat", "/", expr("pointer_escape", refExpr("vars", "/model")))), refExpr("vars", "/model"))),
		setStep("input", "/input", expr("coalesce", client("/input"), []any{})),
		branch("string-input", cond("type", "/input", "string"), []Action{setStep("message-input", "/input", expr("array", expr("object", "role", "user", "content", expr("array", expr("object", "type", "input_text", "text", refExpr("current", "/input"))))))}),
		setStep("stream", "/stream", true), setStep("store", "/store", false),
		branch("cache-hint", testExpr(nonemptyString(fact("/wire_session_id"))), []Action{setStep("cache-key", "/prompt_cache_key", expr("slice", fact("/wire_session_id"), 0, 64))}),
	}
	for _, key := range []string{"service_tier", "tool_choice", "include", "previous_response_id"} {
		path := joinPointer("", key)
		normalize = append(normalize, branch("copy-"+key, all(Condition{Op: "exists", Source: "original", Path: path}, Condition{Op: "ne", Source: "original", Path: path, ValuePresent: true}), []Action{setStep("set-"+key, path, client(path))}))
	}
	normalize = append(normalize, branch("nonempty-tools", testExpr(expr("and", expr("eq", expr("type", client("/tools")), "array"), expr("gt", expr("length", client("/tools")), 0))), []Action{setStep("tools", "/tools", client("/tools"))}))
	reasoning := []Action{setStep("reasoning-object", "/reasoning", map[string]any{}), branch("effort", testExpr(nonemptyString(client("/reasoning/effort"))), []Action{setStep("effort-value", "/reasoning/effort", client("/reasoning/effort"))}), branch("summary", testExpr(nonemptyString(client("/reasoning/summary"))), []Action{setStep("summary-value", "/reasoning/summary", client("/reasoning/summary"))}, branch("default-summary", all(Condition{Op: "exists", Path: "/reasoning/effort"}, testExpr(nonemptyString(fact("/settings/reasoning_summary")))), []Action{setStep("summary-default", "/reasoning/summary", fact("/settings/reasoning_summary"))})), branch("empty-reasoning", cond("eq", "/reasoning", map[string]any{}), []Action{removeStep("omit-reasoning", "/reasoning")})}
	normalize = append(normalize, branch("reasoning-config", Condition{Op: "exists", Source: "original", Path: "/reasoning"}, []Action{branch("reasoning-is-object", Condition{Op: "type", Source: "original", Path: "/reasoning", Value: "object"}, reasoning)}, branch("default-effort", testExpr(nonemptyString(fact("/settings/reasoning_effort"))), []Action{setStep("default-reasoning", "/reasoning", expr("object", "effort", fact("/settings/reasoning_effort"))), branch("default-summary-present", testExpr(nonemptyString(fact("/settings/reasoning_summary"))), []Action{setStep("default-summary-value", "/reasoning/summary", fact("/settings/reasoning_summary"))})})), branch("reasoning-include", all(Condition{Op: "exists", Path: "/reasoning"}, Condition{Op: "not_exists", Path: "/include"}), []Action{setStep("encrypted-include", "/include", []any{"reasoning.encrypted_content"})}))
	finalize := []Action{removeStep("protocol-fields", "/instructions", "/text", "/temperature", "/max_output_tokens", "/parallel_tool_calls", "/prompt_cache_retention", "/prompt_cache_options"), setStep("protocol-store", "/store", false), setStep("protocol-stream", "/stream", true)}
	common := []Action{setStep("empty-headers", "", map[string]any{}), setStep("user-agent", "/user-agent", expr("array", expr("coalesce", fact("/settings/user_agent"), "pi (linux 6.1.0; x64)"))), branch("explicit-session", testExpr(nonemptyString(fact("/wire_session_id"))), []Action{setStep("request-id", "/x-client-request-id", expr("array", fact("/wire_session_id")))})}
	sse := []Action{setStep("accept", "/accept", []any{"application/json"}), setStep("content-type", "/content-type", []any{"application/json"}), setStep("lang", "/x-stainless-lang", []any{"js"}), setStep("sdk-version", "/x-stainless-package-version", []any{"7.19.0"}), setStep("retry-count", "/x-stainless-retry-count", []any{"0"}), setStep("encoding", "/accept-encoding", []any{"gzip, deflate"})}
	for _, pair := range [][2]string{{"os", "stainless_os"}, {"arch", "stainless_arch"}, {"runtime", "stainless_runtime"}, {"runtime-version", "stainless_runtime_version"}} {
		key := pair[0]
		v := fact("/settings/" + pair[1])
		sse = append(sse, branch("platform-"+key, testExpr(nonemptyString(v)), []Action{setStep("platform-value-"+key, "/x-stainless-"+key, expr("array", v))}))
	}
	sse = append(sse, branch("timeout", testExpr(expr("gt", fact("/settings/timeout_seconds"), 0)), []Action{setStep("timeout-value", "/x-stainless-timeout", expr("array", expr("string", fact("/settings/timeout_seconds"))))}), branch("session", testExpr(nonemptyString(fact("/wire_session_id"))), []Action{setStep("session-value", "/session_id", expr("array", fact("/wire_session_id")))}))
	copySDK := []Action{}
	for _, key := range []string{"user-agent", "accept-language", "x-stainless-lang", "x-stainless-package-version", "x-stainless-os", "x-stainless-arch", "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-timeout"} {
		v := expr("index", fact("/client_headers/"+key), 0)
		copySDK = append(copySDK, branch("copy-"+key, testExpr(expr("and", expr("exists", v), nonemptyString(v), expr("lte", expr("length", v), 512), expr("not", expr("contains", v, "\r")), expr("not", expr("contains", v, "\n")))), []Action{setStep("copy-value-"+key, "/"+key, expr("array", v))}))
	}
	sse = append(sse, branch("pi-client", testExpr(expr("eq", expr("slice", expr("coalesce", expr("index", fact("/client_headers/user-agent"), 0), ""), 0, 4), "pi (")), copySDK))
	ws := []Action{setStep("beta", "/openai-beta", []any{"responses_websockets=2026-02-06"}), setStep("originator", "/originator", expr("array", expr("coalesce", fact("/settings/originator"), "pi"))), branch("account-header", testExpr(nonemptyString(fact("/credential_account_id"))), []Action{setStep("account-header-value", "/chatgpt-account-id", expr("array", fact("/credential_account_id")))}), branch("session", testExpr(nonemptyString(fact("/wire_session_id"))), []Action{setStep("session-value", "/session-id", expr("array", fact("/wire_session_id")))})}
	headers := append(common, branch("transport", Condition{Op: "eq", Source: "context", Path: "/transport", Value: "sse"}, sse, step("websocket-headers", "sequence", map[string]any{"steps": ws})))
	return []Rule{base("protocol-request-normalize", "协议：请求构建 / Request construction", PhaseRequestNormalize, normalize), base("protocol-request-finalize", "协议：请求字段 / Request fields", PhaseRequestFinalize, finalize), base("protocol-upstream-headers", "协议：客户端元数据与请求头 / Client metadata and headers", PhaseUpstreamHeaders, headers)}
}
