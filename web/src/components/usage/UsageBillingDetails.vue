<script setup lang="ts">
import { computed } from 'vue'
import type { BillingRates, UsageRecord } from '../../api/types'
import { metric, money } from '../../api/stats-format'
import { useI18n } from '../../i18n'
const props = defineProps<{ record: UsageRecord }>()
const { t } = useI18n()
const billing = computed(() => props.record.billing_details)
const usd = (micros: number | null | undefined) => money(micros == null ? null : micros / 1e6)
function rates(value: BillingRates | null | undefined) {
	return value ? [value.input, value.cached_input, value.cache_write, value.output].map(v => money(v)).join(' / ') : '—'
}
function multipliers(value: BillingRates | null | undefined) {
	return value ? [value.input, value.cached_input, value.cache_write, value.output].map(v => `×${metric(v, 4)}`).join(' / ') : '—'
}
const reasonKeys: Record<string, string> = {
	missing_usage: 'usage.billingMissingUsage', unknown_model: 'usage.billingUnknownModel',
	unpriced_context: 'usage.billingUnpricedContext', unpriced_tier: 'usage.billingUnpricedTier', invalid_usage: 'usage.billingInvalidUsage',
}
</script>

<template>
	<div class="billing-details">
		<strong>{{ t('usage.billingDetails') }}</strong>
		<template v-if="billing">
			<dl>
				<div><dt>{{ t('usage.originalCost') }}</dt><dd>{{ usd(billing.base_cost_micros) }}</dd></div>
				<div><dt>{{ t('usage.baseRates') }}</dt><dd>{{ rates(billing.base_rates) }}</dd></div>
				<div><dt>{{ t('usage.contextBand') }}</dt><dd>{{ billing.long_context ? t('usage.longContext') : t('usage.shortContext') }}<template v-if="billing.context_threshold"> (≥ {{ metric(billing.context_threshold) }})</template></dd></div>
				<div><dt>{{ t('usage.contextRates') }}</dt><dd>{{ rates(billing.context_rates) }}</dd></div>
				<div><dt>{{ t('usage.contextMultipliers') }}</dt><dd>{{ multipliers(billing.context_multipliers) }}</dd></div>
				<div><dt>{{ t('usage.contextCost') }}</dt><dd>{{ usd(billing.context_cost_micros) }}</dd></div>
				<div><dt>{{ t('usage.actualTier') }}</dt><dd>{{ record.service_tier || t('usage.tierUnconfirmed') }}</dd></div>
				<div><dt>{{ t('usage.requestedTier') }}</dt><dd>{{ record.requested_service_tier || '—' }}</dd></div>
				<div><dt>{{ t('usage.fastRates') }}</dt><dd>{{ multipliers(billing.tier_multipliers) }}</dd></div>
				<div><dt>{{ t('usage.effectiveRates') }}</dt><dd>{{ rates(billing.effective_rates) }}</dd></div>
				<div><dt>{{ t('usage.totalCost') }}</dt><dd class="billing-total">{{ money(record.cost_usd) }}</dd></div>
			</dl>
			<p>{{ t('usage.rateOrder') }}</p>
			<p>{{ t('usage.billingFormula') }}</p>
			<p v-if="billing.tier_source === 'default'">{{ t('usage.tierFallbackHint') }}</p>
			<p v-if="billing.unavailable_reason">{{ t(reasonKeys[billing.unavailable_reason] ?? 'usage.billingInvalidUsage') }}</p>
		</template>
		<template v-else><p>{{ t('usage.historicalBilling') }}</p><div>{{ t('usage.totalCost') }}: {{ money(record.cost_usd) }}</div></template>
	</div>
</template>

<style scoped>
.billing-details { display: grid; gap: 9px; color: var(--color-ink); font-size: 11px; text-align: left; white-space: normal; }
.billing-details > strong { font-size: 12px; }
.billing-details dl { display: grid; gap: 7px; margin: 0; }
.billing-details dl > div { display: flex; align-items: baseline; justify-content: space-between; gap: 16px; }
.billing-details dt { color: var(--color-ink-muted); flex-shrink: 0; }
.billing-details dd { margin: 0; text-align: right; overflow-wrap: anywhere; font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.billing-details p { margin: 0; color: var(--color-ink-faint); line-height: 1.5; }
.billing-total { color: var(--color-success); font-weight: 750; }
</style>
