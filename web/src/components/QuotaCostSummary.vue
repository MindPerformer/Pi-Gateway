<script setup lang="ts">
import type { AccountUsageCost, QuotaWindowCost } from '../api/types'
import { money } from '../api/stats-format'
import { useI18n } from '../i18n'
import { formatTime } from '../stores/ui'
import { computed } from 'vue'

const props = defineProps<{ usage: AccountUsageCost | null | undefined; windowCost?: QuotaWindowCost; compact?: boolean; cycleLabel?: string }>()
const { t } = useI18n()
const unknown = computed(() => !!props.windowCost?.unknown || (!!props.usage?.unpriced_requests && !props.usage.priced_requests && !props.usage.repriced_requests))
const partial = computed(() => !!props.windowCost?.partial || !!props.usage?.unpriced_requests)
const recorded = computed(() => props.usage && !unknown.value ? props.usage.cost_micros / 1e6 : null)
const estimated = computed(() => unknown.value ? null : props.windowCost?.estimated_total_usd)
const remaining = computed(() => unknown.value ? null : props.windowCost?.estimated_remaining_usd)
const repriced = computed(() => props.usage?.repriced_requests ? t('pricing.repricedSummary', {count: props.usage.repriced_requests, amount: money((props.usage.repriced_cost_micros ?? 0) / 1e6)}) : '')
function shortMoney(value: number | null | undefined) {
 return money(value == null ? null : Number(value.toFixed(2)))
}
const reasons: Record<string, string> = {
 missing_window: 'quota.costMissingWindow', missing_snapshot: 'quota.costMissingSnapshot',
 query_failed: 'quota.costQueryFailed', expired: 'quota.costExpired',
 no_consumption: 'quota.costNoConsumption',
}
const hint = computed(() => [
 props.cycleLabel,
 `${t(props.windowCost ? 'quota.windowCost' : 'quota.recordedCost')}: ${money(recorded.value)}`,
 props.windowCost ? `${t('quota.estimatedTotal')}: ${money(estimated.value)}` : '',
 props.windowCost ? `${t('quota.estimatedRemaining')}: ${money(remaining.value)}` : '',
 t('quota.costHint'),
 props.windowCost ? t('quota.costEstimateHint') : '',
 props.windowCost?.as_of ? t('quota.costAsOf', { time: formatTime(props.windowCost.as_of) }) : '',
 props.windowCost?.unavailable_reason && reasons[props.windowCost.unavailable_reason] ? t(reasons[props.windowCost.unavailable_reason]!) : '',
 props.usage?.unpriced_requests ? t('quota.unpricedRequests', { count: props.usage.unpriced_requests }) : '',
 unknown.value ? t('pricing.costUnknown') : partial.value ? t('pricing.costPartial') : '',
 repriced.value,
 repriced.value ? t('pricing.repricedHint') : '',
].filter(Boolean).join('\n'))
</script>

<template>
 <span class="cost-summary" :class="{ compact }" :title="hint">
  <span class="cost-used">{{ t('quota.usedShort') }} <strong>{{ shortMoney(recorded) }}</strong></span>
  <span v-if="windowCost" class="cost-estimate">{{ t('quota.estimateShort') }} <strong>{{ shortMoney(estimated) }}</strong></span>
  <span v-if="unknown" class="cost-note">{{ t('pricing.costUnknown') }}</span><span v-else-if="partial" class="cost-note">{{ t('pricing.costPartial') }}</span>
  <span v-if="repriced" class="cost-note" :aria-label="`${repriced}. ${t('pricing.repricedHint')}`">{{ repriced }} · {{ t('pricing.repricedHint') }}</span>
 </span>
</template>

<style scoped>
.cost-summary { display: inline-flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; font-size: 11px; line-height: 1.4; vertical-align: baseline; }
.cost-summary > span { white-space: nowrap; }
.cost-summary strong { font-family: var(--font-mono); font-weight: 650; }
.cost-used { color: var(--color-success); }
.cost-estimate { color: var(--color-info); }
.cost-summary > .cost-note { color: var(--color-warn); white-space: normal; font-size: 10px; }
.compact { gap: 4px 8px; font-size: 10px; }
.cost-summary:focus-visible { outline: 1px solid var(--color-accent); outline-offset: 3px; border-radius: 3px; }
</style>
