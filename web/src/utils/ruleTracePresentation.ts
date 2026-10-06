import type {CaptureRuleTrace} from '../api/types'
import {ruleLabel} from './ruleLabels'

/** Translate implementation-owned names, never replace a user-defined rule name. */
export function traceRuleName(trace: CaptureRuleTrace, locale: string): string {
    const zh = locale === 'zh-CN'
    if (trace.source_kind === 'gateway') {
        const prefix = zh ? '网关内置处理' : 'Gateway processing'
        const name = trace.rule_id === 'gateway:normalize' ? (zh ? '请求规范化' : 'Request normalization')
            : trace.rule_id === 'gateway:pi_shape' ? (zh ? '协议结构约束' : 'Protocol invariants') : trace.rule_name
        return name ? `${prefix}: ${name}` : prefix
    }
    if (trace.source === 'legacy' && trace.rule_name) return ruleLabel('action', trace.rule_name, locale)
    return trace.rule_name || (zh ? '未命名规则' : 'Unnamed rule')
}
