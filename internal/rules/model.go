// Package rules implements immutable, compiled JSON transformation rules.
package rules

import "fmt"

const (
	SchemaVersion         = 2
	PhaseClientRequest    = "client_request"
	PhaseRequestNormalize = "request_normalize"
	PhaseRequestFinalize  = "request_finalize"
	PhaseUpstreamHeaders  = "upstream_headers"
	PhaseRequest          = "request"
	PhaseResponseEvent    = "response_event"
	PhaseResponseBody     = "response_body"
	OnErrorAbort          = "abort"
	OnErrorSkipRule       = "skip_rule"
)

// Rule is the shared persistence, API and editor contract. Lower priorities run first.
// CreatedAt and UpdatedAt are service-managed RFC3339 timestamps, not interpreted by the engine.
type Rule struct {
	SchemaVersion  int       `json:"schema_version"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Enabled        bool      `json:"enabled"`
	Priority       int       `json:"priority"`
	OrderIndex     int64     `json:"order_index"`
	Phase          string    `json:"phase"`
	When           Condition `json:"when"`
	Actions        []Action  `json:"actions"`
	StopAfterMatch bool      `json:"stop_after_match"`
	OnError        string    `json:"on_error"`
	Revision       int64     `json:"revision"`
	CreatedAt      string    `json:"created_at"`
	UpdatedAt      string    `json:"updated_at"`
	LegacyName     string    `json:"legacy_name,omitempty"`
	Source         string    `json:"source,omitempty"`
}

type Action struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// Condition.ValuePresent distinguishes explicit JSON null from a missing value.
// The JSON decoder sets it automatically. Go callers constructing a null comparison
// must set ValuePresent=true. Non-nil Value is always considered present.
type Condition struct {
	Op              string      `json:"op"`
	Source          string      `json:"source,omitempty"`
	Path            string      `json:"path,omitempty"`
	Encoding        string      `json:"encoding,omitempty"`
	Value           any         `json:"value,omitempty"`
	Conditions      []Condition `json:"conditions,omitempty"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
	DotAll          bool        `json:"dot_all,omitempty"`
	Multiline       bool        `json:"multiline,omitempty"`
	ValuePresent    bool        `json:"-"`
	present         map[string]bool
}

// Input is borrowed: Apply never mutates it or any reachable payload/context.
// Body accepts JSON values and *piwire.OrderedMap (whose key order is preserved).
// Context supports application facts, plus engine-provided original_model/model,
// event_type and (inside array predicates) item_index. Request context cannot use account_id.
type Input struct {
	Body       any            `json:"body"`
	ClientBody any            `json:"client_body,omitempty"`
	Context    map[string]any `json:"context,omitempty"`
	Model      string         `json:"model,omitempty"`
	EventType  string         `json:"event_type,omitempty"`
	Trace      bool           `json:"trace,omitempty"`
	// TraceMaxBytes and MaxTraces bound in-process diagnostic collection. Zero uses defaults.
	TraceMaxBytes int `json:"trace_max_bytes,omitempty"`
	MaxTraces     int `json:"max_traces,omitempty"`
	// ConditionTrace is opt-in, independent of the production action trace switch.
	ConditionTrace bool `json:"condition_trace,omitempty"`
	// Unavailable contains source JSON pointers missing from a historical sample.
	// It is internal-only and checked lazily, preserving condition short-circuiting.
	Unavailable []string `json:"-"`
}

type Result struct {
	Body                  any              `json:"body"`
	Model                 string           `json:"model"`
	Changed               bool             `json:"changed"`
	Blocked               bool             `json:"blocked"`
	Status                int              `json:"status,omitempty"`
	Reason                string           `json:"reason,omitempty"`
	Dropped               bool             `json:"dropped"`
	Traces                []Trace          `json:"traces,omitempty"`
	TraceOmitted          int              `json:"trace_omitted,omitempty"`
	ConditionTraces       []ConditionTrace `json:"condition_traces,omitempty"`
	ConditionTraceOmitted int              `json:"condition_trace_omitted,omitempty"`
}

// ConditionTrace paths are relative to the individual rule, not the ruleset.
// Matched is absent for skipped/error nodes; ItemIndex is the pre-filter index.
type ConditionTrace struct {
	RuleID    string `json:"rule_id"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	Matched   *bool  `json:"matched,omitempty"`
	Error     string `json:"error,omitempty"`
	ItemIndex *int   `json:"item_index,omitempty"`
}

// MissingInputError is not a rule failure: a historical sample lacks needed facts.
type MissingInputError struct{ Path string }

func (e *MissingInputError) Error() string { return "sample lacks required input: " + e.Path }

// Change records actual structural changes; missing and explicit null remain distinct.
// Values are bounded/redacted for trace storage, never used to execute rules.
type Change struct {
	Path         string `json:"path"`
	Operation    string `json:"operation"`
	BeforeExists bool   `json:"before_exists"`
	AfterExists  bool   `json:"after_exists"`
	Before       any    `json:"before"`
	After        any    `json:"after"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type Trace struct {
	Source         string   `json:"source,omitempty"`
	RuleID         string   `json:"rule_id"`
	RuleName       string   `json:"rule_name"`
	Revision       int64    `json:"revision"`
	Priority       int      `json:"priority"`
	Phase          string   `json:"phase"`
	ActionID       string   `json:"action_id,omitempty"`
	ActionType     string   `json:"action_type,omitempty"`
	ActionIndex    int      `json:"action_index"`
	Matched        bool     `json:"matched"`
	Status         string   `json:"status"`
	RolledBack     bool     `json:"rolled_back,omitempty"`
	Error          string   `json:"error,omitempty"`
	DurationNS     int64    `json:"duration_ns"`
	Changes        []Change `json:"changes,omitempty"`
	OmittedChanges int      `json:"omitted_changes,omitempty"`
	ActionPath     string   `json:"action_path,omitempty"`
	ItemPath       string   `json:"item_path,omitempty"`
	EventType      string   `json:"event_type,omitempty"`
	EventID        string   `json:"event_id,omitempty"`
	Sequence       any      `json:"sequence,omitempty"`
}

// ValidationError carries a precise JSON Pointer into the submitted rule(s).
type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Message) }
func invalid(path, format string, args ...any) error {
	return &ValidationError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// FieldSpec is consumed both by the validator and graphical field renderer.
// Type is one of string, boolean, integer, pointer, string_array, pointer_array,
// value, value_array, condition, condition_array. Value supports all JSON types.
type FieldSpec struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Default     any               `json:"default"`
	Required    bool              `json:"required"`
	Enum        []string          `json:"enum,omitempty"`
	Minimum     *int64            `json:"minimum,omitempty"`
	Maximum     *int64            `json:"maximum,omitempty"`
	NonEmpty    bool              `json:"non_empty,omitempty"`
	Control     string            `json:"control"`
	DependsOn   map[string]any    `json:"depends_on,omitempty"`
	Examples    []any             `json:"examples,omitempty"`
	Help        map[string]string `json:"help,omitempty"`
	EnumHelp    map[string]string `json:"enum_help,omitempty"`
}

type Capability struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Phases      []string    `json:"phases"`
	Fields      []FieldSpec `json:"fields"`
	Deprecated  bool        `json:"deprecated,omitempty"`
}

type CatalogSpec struct {
	SchemaVersion    int            `json:"schema_version"`
	Phases           []string       `json:"phases"`
	RuleFields       []FieldSpec    `json:"rule_fields"`
	Actions          []Capability   `json:"actions"`
	Conditions       []Capability   `json:"conditions"`
	ValueExpressions []Capability   `json:"value_expressions"`
	ContextFields    []FieldSpec    `json:"context_fields"`
	Examples         []Rule         `json:"examples"`
	Limits           map[string]int `json:"limits"`
}
