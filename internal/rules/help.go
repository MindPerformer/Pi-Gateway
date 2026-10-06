package rules

import "sort"

func fieldExamples(f FieldSpec) []any {
	if len(f.Enum) > 0 {
		out := []any{}
		for _, v := range f.Enum {
			out = append(out, v)
		}
		return out
	}
	switch f.Type {
	case "pointer":
		return []any{"/input/0/content/0/text", "/metadata/a~1b"}
	case "pointer_array":
		return []any{[]any{"/metadata/private", "/input/0"}}
	case "boolean":
		return []any{true, false}
	case "integer":
		n := int64(0)
		if f.Minimum != nil {
			n = *f.Minimum
		}
		return []any{n}
	case "value":
		return []any{map[string]any{"enabled": true}, refExpr("client", "/metadata"), expr("length", refExpr("current", "/input"))}
	case "value_array":
		return []any{[]any{"hello", 42}}
	case "string_array":
		return []any{[]any{"metadata", "truncation"}}
	case "condition":
		return []any{map[string]any{"op": "type", "source": "current", "path": "/input", "value": "array"}}
	case "condition_array":
		return []any{[]any{map[string]any{"op": "exists", "path": "/metadata"}}}
	case "action_array":
		return []any{[]any{map[string]any{"id": "set-value", "type": "json_set", "params": map[string]any{"path": "/metadata/policy", "value": "example", "create_parents": true}}}}
	}
	if f.Default != nil {
		if s, ok := f.Default.(string); ok && s != "" {
			return []any{s}
		}
	}
	switch f.Name {
	case "pattern":
		return []any{"(?s)<tag>.*?</tag>"}
	case "replacement":
		return []any{"${1}-updated", ""}
	case "name", "bind":
		return []any{"message"}
	case "model":
		return []any{"example-model"}
	case "created_at", "updated_at":
		return []any{"2026-10-07T00:00:00Z"}
	}
	return []any{"example"}
}
func optionHelp(v string) string {
	m := map[string]string{
		"current": "读取前序步骤的结果。", "original": "读取当前阶段或遍历元素开始处理时的原始值。", "client": "读取最初客户端 body，只读。", "context": "读取网关事实；阶段决定可用字段。", "vars": "读取局部变量，如 /message/index。", "item": "读取当前遍历元素的原始值。",
		"ignore": "目标不存在时跳过；null 仍视为存在。", "error": "目标不存在时报错并回滚整条规则。", "abort": "失败回滚当前规则并终止阶段。", "skip_rule": "失败回滚当前规则后继续下一条。", "overwrite": "替换已存在值，包括 null。", "keep": "保留已存在值，包括 null。", "value": "保留 JSON 类型。", "json": "序列化为 JSON 字符串，字符串包含引号。", "literal": "按原文匹配，不解释捕获组。", "regex": "使用 Go RE2 正则。", "remove_matches": "删除条件成立的数组元素。", "keep_matches": "只保留条件成立的数组元素。",
		"client_request": "规范化之前的客户端 body。", "request_normalize": "协议请求构建，输入为客户端 body。", "request": "规范化后的请求；路由前。", "request_finalize": "路由前的协议字段修正。", "upstream_headers": "发送前请求头，context/transport 为 sse 或 websocket。", "response_event": "逐个响应事件；不会自动同步终态快照。", "response_body": "非流式聚合后的正文。",
	}
	if s, ok := m[v]; ok {
		return s
	}
	return "选择 " + v + "；具体行为与其他参数关系见本字段说明。"
}
func computedCapabilities() []Capability {
	names := []string{}
	for name := range expressionArity {
		names = append(names, name)
	}
	sort.Strings(names)
	return []Capability{{ID: "computed", Label: "计算值 / Computed value", Description: "{$expr:{op,args}}；所有参数支持嵌套引用与计算；对象使用 object 的键值对数组构造，普通 JSON 对象永远是字面量。", Phases: phases, Fields: []FieldSpec{field("op", "string", "运算 / Operator", "get 动态 JSON Pointer；index 读取数组下标；object 为键值对；regex_replace 的三个参数为原文、RE2 模式、替换值。", nil, true, names...), field("args", "value_array", "参数 / Arguments", "参数按顺序求值；and/or/coalesce 短路；不存在与 null 区分。", nil, true)}}}
}
