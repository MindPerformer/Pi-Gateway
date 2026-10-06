import type {JsonValue, Rule, RuleFieldError, RulePhase} from './rules'
import type {CaptureRuleTrace} from './types'

export interface RuleConditionTrace {
    rule_id: string
    path: string
    status: string
    matched?: boolean
    error?: string
    item_index?: number
}

export interface CaptureSimulationRequest {
    capture_id: number
    rule: Rule
    phase: RulePhase
    scope: 'ruleset' | 'rule'
    enable_draft?: boolean
    frame_seq?: number
    batch?: boolean
    after_seq?: number
    limit?: number
    expected_version?: number
    batch_token?: string
}

export interface CaptureSimulationExecution {
    body?: JsonValue
    model: string
    changed: boolean
    blocked: boolean
    dropped: boolean
    status?: number
    reason?: string
    traces?: CaptureRuleTrace[]
    trace_omitted?: number
    condition_traces?: RuleConditionTrace[]
    condition_trace_omitted?: number
}

export interface CaptureSimulationSample {
    frame_seq?: number
    event_type?: string
    source: 'checkpoint' | 'recorded_event' | 'reconstructed'
    warnings?: string[]
    before?: JsonValue
    result: CaptureSimulationExecution
    error?: string
    preview_truncated?: boolean
}

export interface CaptureSimulationResponse {
    valid: boolean
    capture_id: number
    draft_rule_id: string
    rules_version: number
    phase: RulePhase
    results: CaptureSimulationSample[]
    warnings?: string[]
    errors?: RuleFieldError[]
    has_more: boolean
    next_after_seq?: number
    total: number
    batch_token?: string
}

/** Only the selected, current simulation result may decorate the canvas. */
export interface RuleCanvasDiagnostics {
    ruleId: string
    traces: CaptureRuleTrace[]
    conditions: RuleConditionTrace[]
}

export interface RuleDebugFocus {
    ruleId: string
    actionId?: string
    path?: string
}
