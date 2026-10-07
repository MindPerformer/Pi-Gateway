<script setup lang="ts">
import type { AccountUsageCost, QuotaWindowCost } from '../api/types'
import { money } from '../api/stats-format'
import { useI18n } from '../i18n'
import { formatTime } from '../stores/ui'
import { computed } from 'vue'

const props = defineProps<{ usage: AccountUsageCost | null | undefined; windowCost?: QuotaWindowCost; compact?: boolean; cycleLabel?: string }>()
const { t } = useI18n()
function costLabel(usage: AccountUsageCost | null | undefined) {
	return money(usage && (usage.priced_requests > 0 || usage.unpriced_requests === 0) ? usage.cost_micros / 1e6 : null)
}
const reasons: Record<string, string> = {
	missing_window: 'quota.costMissingWindow', missing_snapshot: 'quota.costMissingSnapshot',
	query_failed: 'quota.costQueryFailed', expired: 'quota.costExpired', unpriced: 'quota.costUnpriced',
	no_usage: 'quota.costNoUsage', no_consumption: 'quota.costNoConsumption',
}
const hint = computed(() => [
	props.cycleLabel,
	t('quota.costHint'),
	props.windowCost ? t('quota.costEstimateHint') : '',
	props.windowCost?.as_of ? t('quota.costAsOf', { time: formatTime(props.windowCost.as_of) }) : '',
	props.windowCost?.unavailable_reason ? t(reasons[props.windowCost.unavailable_reason] ?? 'quota.costQueryFailed') : '',
].filter(Boolean).join('\n'))
</script>

<template>
	<div class="cost-summary" :class="{ compact }" :title="hint">
		<span v-if="cycleLabel" class="cost-note">{{ cycleLabel }}</span>
		<div class="cost-row"><span>{{ t(windowCost ? 'quota.windowCost' : 'quota.recordedCost') }}</span><strong>{{ costLabel(usage) }}</strong></div>
		<template v-if="windowCost">
			<div class="cost-row" :title="t('quota.costEstimateHint')"><span>{{ t('quota.estimatedTotal') }}</span><strong>{{ money(windowCost.estimated_total_usd) }}</strong></div>
			<div v-if="!compact" class="cost-row" :title="t('quota.costEstimateHint')"><span>{{ t('quota.estimatedRemaining') }}</span><strong>{{ money(windowCost.estimated_remaining_usd) }}</strong></div>
			<p v-if="!compact && windowCost.as_of" class="cost-note">{{ t('quota.costAsOf', { time: formatTime(windowCost.as_of) }) }}</p>
			<p v-if="!compact && windowCost.unavailable_reason" class="cost-note">{{ t(reasons[windowCost.unavailable_reason] ?? 'quota.costQueryFailed') }}</p>
		</template>
		<p v-if="!compact && usage?.unpriced_requests" class="cost-note">{{ t('quota.unpricedRequests', { count: usage.unpriced_requests }) }}</p>
		<span v-else-if="compact && usage?.unpriced_requests" class="cost-note" :title="t('quota.unpricedRequests', { count: usage.unpriced_requests })">{{ t('quota.partialCost') }}</span>
	</div>
</template>

<style scoped>
.cost-summary { display: grid; gap: 5px; font-size: 11px; color: var(--color-ink-muted); }
.cost-row { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 12px; }
.cost-row strong { color: var(--color-success); font-family: var(--font-mono); font-weight: 650; overflow-wrap: anywhere; }
.cost-note { margin: 0; font-size: 10px; color: var(--color-ink-faint); }
.compact { font-size: 10px; margin-top: 7px; }
</style>
