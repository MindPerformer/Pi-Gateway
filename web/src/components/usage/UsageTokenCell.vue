<script setup lang="ts">
import { Archive, ArrowDown, ArrowUp } from 'lucide-vue-next'
import type { UsageRecord } from '../../api/types'
import { metric } from '../../api/stats-format'
import { useI18n } from '../../i18n'

defineProps<{
	record: Pick<UsageRecord, 'input_tokens' | 'output_tokens' | 'cached_tokens' | 'total_tokens'>
	showTotal?: boolean
}>()
const { t } = useI18n()
</script>

<template>
	<div class="token-cell">
		<span class="token-input" :title="t('stats.inputTokens')"><ArrowDown :size="12" aria-hidden="true" />{{ metric(record.input_tokens) }}</span>
		<span class="token-output" :title="t('stats.outputTokens')"><ArrowUp :size="12" aria-hidden="true" />{{ metric(record.output_tokens) }}</span>
		<span class="token-cached" :title="t('stats.cachedTokens')"><Archive :size="12" aria-hidden="true" />{{ metric(record.cached_tokens) }}</span>
		<span v-if="showTotal" class="token-total" :title="t('stats.totalTokens')">Σ {{ metric(record.total_tokens) }}</span>
	</div>
</template>

<style scoped>
.token-cell { display: inline-grid; grid-template-columns: auto auto; align-items: center; justify-content: end; gap: 5px 8px; font-family: var(--font-mono); font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; line-height: 1; white-space: nowrap; }
.token-cell > span { display: inline-flex; align-items: center; justify-content: flex-end; gap: 4px; }
.token-input { color: var(--color-success); }
.token-output, .token-cached { color: var(--color-info); }
.token-cached { grid-column: 1 / -1; }
.token-total { grid-column: 1 / -1; color: var(--color-ink-muted); font-size: 11px; }
</style>
