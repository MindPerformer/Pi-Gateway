<script setup lang="ts">
import type { AccountUsageCost, QuotaWindowCost } from '../api/types'
import { money } from '../api/stats-format'
import { useI18n } from '../i18n'
import { formatTime } from '../stores/ui'
import { computed } from 'vue'

const props = defineProps<{ usage: AccountUsageCost | null | undefined; windowCost?: QuotaWindowCost; compact?: boolean; cycleLabel?: string }>()
const { t } = useI18n()
const recorded = computed(() => props.usage ? props.usage.cost_micros / 1e6 : null)
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
 props.windowCost ? `${t('quota.estimatedTotal')}: ${money(props.windowCost.estimated_total_usd)}` : '',
 props.windowCost ? `${t('quota.estimatedRemaining')}: ${money(props.windowCost.estimated_remaining_usd)}` : '',
 t('quota.costHint'),
 props.windowCost ? t('quota.costEstimateHint') : '',
 props.windowCost?.as_of ? t('quota.costAsOf', { time: formatTime(props.windowCost.as_of) }) : '',
 props.windowCost?.unavailable_reason && reasons[props.windowCost.unavailable_reason] ? t(reasons[props.windowCost.unavailable_reason]!) : '',
 props.usage?.unpriced_requests ? t('quota.unpricedRequests', { count: props.usage.unpriced_requests }) : '',
].filter(Boolean).join('\n'))
</script>

<template>
 <span class="cost-summary" :class="{ compact }" :title="hint">
  <span class="cost-used">{{ t('quota.usedShort') }} <strong>{{ shortMoney(recorded) }}</strong></span>
  <span v-if="windowCost" class="cost-estimate">{{ t('quota.estimateShort') }} <strong>{{ shortMoney(windowCost.estimated_total_usd) }}</strong></span>
 </span>
</template>

<style scoped>
.cost-summary { display: inline-flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; font-size: 11px; line-height: 1.4; vertical-align: baseline; }
.cost-summary > span { white-space: nowrap; }
.cost-summary strong { font-family: var(--font-mono); font-weight: 650; }
.cost-used { color: var(--color-success); }
.cost-estimate { color: var(--color-info); }
.compact { gap: 4px 8px; font-size: 10px; }
.cost-summary:focus-visible { outline: 1px solid var(--color-accent); outline-offset: 3px; border-radius: 3px; }
</style>
