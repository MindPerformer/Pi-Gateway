<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Capture, CaptureRuleTrace } from '../../api/types'
import { useI18n } from '../../i18n'
import { isRuleChange, lastRuleWriters, ruleChangeSnapshot, ruleTraces } from '../../utils/captureInspection'
import { useCaptureLocale } from './captureLocale'
import CaptureDiff from './CaptureDiff.vue'
import Badge from '../Badge.vue'
import { ruleLabel } from '../../utils/ruleLabels'
import { traceRuleName } from '../../utils/ruleTracePresentation'

const props = defineProps<{ capture: Capture }>()
const { t, locale } = useI18n()
const { c } = useCaptureLocale()
const limit = ref(50)
const expanded = ref(new Set<number>())
const traces = computed(() => ruleTraces(props.capture))
const visible = computed(() => traces.value.slice(0, limit.value))
const finalWriters = computed(() => lastRuleWriters(props.capture))
const eventFrames = computed(() => {
    const result = new Map<string, {before: number[]; after: number[]}>()
    for (const frame of props.capture.response_frames ?? []) {
        if (!frame.rule_event_id) continue
        const item = result.get(frame.rule_event_id) ?? {before: [], after: []}
        if (frame.dir === 'in') item.before.push(frame.seq)
        if (frame.dir === 'client_out') item.after.push(frame.seq)
        result.set(frame.rule_event_id, item)
    }
    return result
})
const statusKeys: Record<string, string> = {
    not_matched: 'statusNotMatched', no_change: 'statusNoChange', changed: 'statusChanged',
    skipped: 'statusSkipped', error: 'statusError', blocked: 'statusBlocked', dropped: 'statusDropped', rolled_back: 'statusRolledBack',
}
function actionLabel(action?: string) { return action ? ruleLabel('action', action, locale.value) : '' }
function phaseLabel(phase?: string) {
    const key = phase === 'request' ? 'phaseRequest' : phase === 'response_event' ? 'phaseResponseEvent' : phase === 'response_body' ? 'phaseResponseBody' : ''
    return key ? t(`capture.rules.${key}`) : phase || c('unrecorded')
}
function statusLabel(trace: CaptureRuleTrace) {
    if (trace.rolled_back) return t('capture.rules.statusRolledBack')
    const key = Object.prototype.hasOwnProperty.call(statusKeys, trace.status ?? '') ? statusKeys[trace.status!] : ''
    return key ? t(`capture.rules.${key}`) : trace.status || c('unrecorded')
}
function tone(trace: CaptureRuleTrace) {
    return trace.error || trace.status === 'error' || trace.status === 'blocked' ? 'danger'
        : trace.rolled_back || trace.status === 'dropped' ? 'warn' : trace.status === 'changed' ? 'info' : 'neutral'
}
function toggle(index: number, event: Event) {
    if ((event.target as HTMLDetailsElement).open) expanded.value.add(index)
    else expanded.value.delete(index)
}
</script>

<template>
    <section class="rule-trace-panel" aria-labelledby="rule-trace-title">
        <div class="trace-heading"><h2 id="rule-trace-title">{{ t('capture.rules.title') }}</h2><span>{{ t('capture.rules.version', { version: capture.rules_version ?? 0 }) }}</span></div>
        <p v-if="capture.rules_trace_truncated || capture.rules_trace_omitted" class="notice notice-warning" role="status">{{ t('capture.rules.truncated', { count: capture.rules_trace_omitted ?? 0 }) }}</p>
        <p v-if="!traces.length" class="trace-hint">{{ !capture.rules_version && !capture.rules_trace_truncated ? t('capture.rules.legacy') : t('capture.rules.noChanges') }}</p>
        <p v-else class="trace-hint">{{ t('capture.rules.imprecise') }}</p>
        <details v-for="(trace, index) in visible" :key="index" class="card trace-step" :data-rule-status="trace.status" @toggle="toggle(index, $event)">
            <summary>
                <span class="trace-order">{{ index + 1 }}.</span><strong>{{ traceRuleName(trace, locale) }}</strong>
                <Badge :tone="tone(trace)">{{ statusLabel(trace) }}</Badge>
                <span>{{ phaseLabel(trace.phase) }}</span><code>{{ actionLabel(trace.action_type) }}</code>
            </summary>
            <div v-if="expanded.has(index)" class="trace-body">
                <div class="trace-meta">
                    <code v-if="trace.rule_id">{{ trace.rule_id }}</code>
                    <span v-if="trace.revision !== undefined">{{ t('capture.rules.revision', { value: trace.revision }) }}</span>
                    <span v-if="trace.priority !== undefined">{{ t('capture.rules.priority', { value: trace.priority }) }}</span>
                    <span>{{ t('capture.rules.version', { version: trace.rules_version ?? capture.rules_version ?? 0 }) }}</span>
                    <span v-if="trace.action_id">{{ t('capture.rules.action', { index: (trace.action_index ?? 0) + 1 }) }} · <code>{{ trace.action_id }}</code></span>
                    <span v-if="typeof trace.duration_ns === 'number'">{{ (trace.duration_ns / 1_000_000).toFixed(3) }} ms</span>
                </div>
                <p v-if="trace.event_id" class="trace-hint">{{ t('capture.rules.event') }}: <code>{{ trace.event_id }}</code> · {{ trace.event_type || '' }}<span v-if="trace.sequence !== undefined"> #{{ trace.sequence }}</span></p>
                <p v-if="trace.event_id && eventFrames.has(trace.event_id)" class="trace-hint">{{ c('upstreamIn') }}: {{ eventFrames.get(trace.event_id)?.before.map(seq => `#${seq}`).join(', ') || c('unrecorded') }} → {{ c('clientOut') }}: {{ eventFrames.get(trace.event_id)?.after.map(seq => `#${seq}`).join(', ') || c('unrecorded') }}</p>
                <p v-if="trace.status === 'blocked'" class="notice notice-error">{{ t('capture.rules.blocked') }}</p>
                <p v-if="trace.error" class="notice notice-error">{{ trace.error }}</p>
                <p v-if="trace.trace_truncated || trace.omitted_changes" class="notice notice-warning">{{ t('capture.rules.omittedChanges', { count: trace.omitted_changes ?? 0 }) }}</p>
                <div v-for="(change, changeIndex) in isRuleChange(trace) ? trace.changes ?? [] : []" :key="changeIndex" class="trace-change">
                    <div class="trace-meta"><code>{{ change.operation }} {{ change.path || '/' }}</code><span v-if="finalWriters.has(`${index}:${changeIndex}`)">{{ t('capture.rules.finalWriter') }}</span></div>
                    <CaptureDiff :title="change.path || '/'" :before="ruleChangeSnapshot(change, 'before')" :after="ruleChangeSnapshot(change, 'after')" :before-label="change.before_exists === false ? t('capture.rules.missing') : c('before')" :after-label="change.after_exists === false ? t('capture.rules.missing') : c('after')" :partial="Boolean(change.truncated || trace.trace_truncated)" initial-open />
                </div>
            </div>
        </details>
        <button v-if="traces.length > limit" type="button" class="btn" @click="limit += 50">{{ c('showMore', { count: traces.length - limit }) }}</button>
    </section>
</template>

<style scoped>
.rule-trace-panel { display: grid; gap: 10px; min-width: 0; }.trace-heading, .trace-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; }.trace-heading h2 { font-size: 14px; margin: 0; }.trace-heading > span, .trace-meta, .trace-hint { font-size: 11px; color: var(--color-ink-muted); line-height: 1.6; overflow-wrap: anywhere; }.trace-hint { margin: 0; }.trace-step { min-width: 0; overflow: hidden; }.trace-step > summary { padding: 12px; cursor: pointer; display: flex; align-items: center; flex-wrap: wrap; gap: 7px; font-size: 12px; overflow-wrap: anywhere; }.trace-order { color: var(--color-ink-faint); }.trace-body { border-top: 1px solid var(--color-line); padding: 12px; display: grid; gap: 10px; min-width: 0; }.trace-change { display: grid; gap: 6px; min-width: 0; }.trace-body code { overflow-wrap: anywhere; }.notice { margin: 0; overflow-wrap: anywhere; }
</style>
