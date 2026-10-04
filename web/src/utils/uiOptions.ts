import {translateNow} from '../i18n'

// Values remain the wire/API enums; only labels and explanations are localized.
export const transportValues = ['passthrough', 'sse', 'websocket', 'websocket-cached', 'auto'] as const
export const strategyValues = ['smart', 'quota_reset_priority', 'round_robin', 'sticky', 'least_inflight', 'single'] as const

function optionText(kind: 'transport' | 'strategy', value: string, part: 'label' | 'description'): string {
    const normalized = value.trim().toLowerCase()
    const choices: readonly string[] = kind === 'transport' ? transportValues : strategyValues
    // "ws" is an account-level override and always uses pooled WebSockets.
    const canonical = kind === 'transport' && normalized === 'ws' ? 'websocket-cached' : normalized
    if (!canonical) return translateNow('ui.options.unavailable')
    if (!choices.includes(canonical)) return translateNow('ui.options.unknown')
    return translateNow(`ui.${kind}.${canonical}.${part}`)
}

export function transportLabel(value: string): string {
    return optionText('transport', value, 'label')
}

export function transportDescription(value: string): string {
    return optionText('transport', value, 'description')
}

export function strategyLabel(value: string): string {
    return optionText('strategy', value, 'label')
}

export function strategyDescription(value: string): string {
    return optionText('strategy', value, 'description')
}

export function accountStatusLabel(value: string): string {
    if (value === 'disabled') return translateNow('common.disabled')
    const statuses = ['ready', 'expired', 'invalid', 'banned', 'quota_exhausted', 'unknown']
    return translateNow(`ui.accountStatus.${statuses.includes(value) ? value : 'unknown'}`)
}
