import type {LocalizedHelp, RuleCapability, RuleField, RulePhase, RuleRenderer, RuleSchema} from '../api/rules'
import {help, ruleSchema} from './ruleSchema'

export interface BackendField {
    name: string;
    type: string;
    label?: string;
    description: string;
    default?: unknown;
    required?: boolean
    enum?: string[];
    minimum?: number;
    maximum?: number;
    non_empty?: boolean;
    depends_on?: Record<string, unknown>;
    examples?: unknown[]
}

export interface BackendCapability {
    id: string;
    label?: string;
    description: string;
    phases?: RulePhase[];
    fields: BackendField[]
}

export interface BackendRuleSchema {
    schema_version: number;
    phases: RulePhase[];
    rule_fields: BackendField[];
    conditions: BackendCapability[];
    actions: BackendCapability[]
    value_expressions: BackendCapability[];
    context_fields: BackendField[];
    examples?: RuleSchema['examples'];
    limits?: Record<string, number>
}

export const backendRenderers: Record<string, RuleRenderer> = {
    string: 'string',
    boolean: 'boolean',
    integer: 'number',
    pointer: 'string',
    string_array: 'strings',
    pointer_array: 'strings',
    value: 'value',
    value_array: 'values',
    condition: 'condition',
    condition_array: 'condition_array',
    action_array: 'action_array',
}
export const readonlyRuleFields = ['id', 'revision', 'order_index', 'created_at', 'updated_at', 'legacy_name', 'source']
const metadataHelp: Record<string, LocalizedHelp> = {
    source: help('Read-only provenance recorded by the server.', '服务端记录的只读来源元数据。'),
    id: help('Stable server-assigned rule identity; not editable.', '服务端生成的稳定规则 ID，不可编辑。'),
    revision: help('Optimistic concurrency revision. Every update requires the expected revision.', '乐观并发修订号；更新必须携带预期修订号。'),
    order_index: help('Server-managed stable ordering among equal priorities. Use adjacent move.', '服务端管理的同优先级稳定次序；请使用相邻移动。'),
    created_at: help('Server creation time (RFC3339).', '服务端创建时间（RFC3339）。'),
    updated_at: help('Server last update time (RFC3339).', '服务端更新时间（RFC3339）。'),
    legacy_name: help('Migration source. This is not a rule type or a unique name.', '旧中间件迁移来源，不是规则类型或唯一名称。'),
}
export const contextHelp: Record<string, LocalizedHelp> = {
    original_model: help('Original model before rule execution; read-only.', '规则执行前原始模型，只读。'),
    model: help('Current routed model, synchronized with the payload.', '当前路由模型，与载荷同步。'),
    request_path: help('Authenticated client request path.', '已鉴权客户端请求路径。'),
    request_method: help('Client HTTP method.', '客户端 HTTP 方法。'),
    api_key_id: help('Authenticated API key ID (number); no credentials are exposed.', '已鉴权 API Key ID（数字），不暴露凭据。'),
    client_protocol: help('Client protocol provided by the gateway.', '网关提供的客户端协议。'),
    account_id: help('Selected account ID; response phases only. Request references are invalid.', '已选择账号 ID；仅响应阶段可用，请求阶段引用非法。'),
    upstream_protocol: help('Actual upstream protocol; response phases only.', '实际上游协议，仅响应阶段可用。'),
    event_type: help('Original response event type; response phases only.', '原始响应事件类型，仅响应阶段可用。'),
    item_index: help('Original zero-based array index; only available inside array_filter predicates.', '数组原始零基索引，仅在 array_filter 谓词中可用。'),
}
export const valueExpressionHelp: Record<string, LocalizedHelp> = {
    literal: help('Plain JSON including null, nested arrays and objects. Object keys $ref/$literal require explicit escaping.', '普通 JSON，包括 null、嵌套数组和对象；含 $ref／$literal 键时需显式转义。'),
    reference: help('{$ref:{source,path,encoding}} reads context at execution time. Missing references fail actions or do not match conditions.', '{$ref:{source,path,encoding}} 在执行时取值；缺失引用使动作失败、条件不匹配。'),
    literal_escape: help('{$literal:JSON} preserves the entire value without expression interpretation.', '{$literal:JSON} 保留完整字面量，不解释内部表达式。'),
}

export function adaptSchema(raw: BackendRuleSchema): RuleSchema {
    if (raw.schema_version !== 1) throw new Error('Unsupported rule schema version')
    const convert = (f: BackendField, mirror?: RuleField, extraHelp?: LocalizedHelp, readonly = false): RuleField => {
        const type = f.enum?.length ? 'enum' : backendRenderers[f.type]
        if (f.enum?.some(option => !mirror?.enum?.includes(option))) throw new Error(`Missing enum help coverage: ${f.name}`)
        if (!type) throw new Error(`Unsupported renderer: ${f.type}`)
        if (!mirror && !extraHelp) throw new Error(`Missing bilingual field help: ${f.name}`)
        const result: RuleField = {
            name: f.name,
            type,
            required: f.required,
            enum: f.enum,
            min: f.minimum,
            max: f.maximum,
            description: mirror?.description ?? extraHelp!,
            examples: f.examples?.length ? f.examples : mirror?.examples,
            depends_on: f.depends_on,
            non_empty: f.non_empty,
            readonly,
        }
        // nil in the backend schema means "no default", except value:null is a real literal.
        if (f.default !== undefined && f.default !== null) result.default = f.default
        else if (type === 'value') result.default = null
        return result
    }
    const convertCaps = (caps: BackendCapability[], mirrorCaps: RuleCapability[]): RuleCapability[] => caps.map(c => {
        const mirror = mirrorCaps.find(x => x.id === c.id)
        if (!mirror) throw new Error(`Missing capability renderer/help: ${c.id}`)
        return {
            id: c.id,
            phases: c.phases,
            description: mirror.description,
            fields: c.fields.map(f => convert(f, mirror.fields.find(x => x.name === f.name), f.name === 'conditions' ? help('Ordered recursive children; all/any non-empty, not exactly one.', '有序递归子条件；all／any 非空，not 恰好一个。') : undefined))
        }
    })
    const valueExpressions = raw.value_expressions.map(c => {
        const description = valueExpressionHelp[c.id]
        if (!description) throw new Error(`Missing expression renderer/help: ${c.id}`)
        return {
            id: c.id,
            phases: c.phases,
            description,
            fields: c.fields.map(f => convert(f, ruleSchema.value_fields.find(x => x.name === f.name)))
        }
    })
    return {
        schema_version: raw.schema_version,
        phases: raw.phases,
        sources: ['current', 'client', 'context', 'item'],
        encodings: ['value', 'json'],
        rule_fields: raw.rule_fields.map(f => convert(f, ruleSchema.rule_fields.find(x => x.name === f.name), metadataHelp[f.name], readonlyRuleFields.includes(f.name))),
        conditions: convertCaps(raw.conditions, ruleSchema.conditions),
        actions: convertCaps(raw.actions, ruleSchema.actions),
        condition_fields: ruleSchema.condition_fields,
        value_fields: ruleSchema.value_fields,
        value_expressions: valueExpressions,
        context_fields: raw.context_fields.map(f => convert(f, undefined, contextHelp[f.name])),
        examples: raw.examples,
    }
}
