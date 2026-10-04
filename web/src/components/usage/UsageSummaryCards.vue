<script setup lang="ts">
import { computed } from 'vue'
import { Activity, Database, FileText, Timer } from 'lucide-vue-next'
import type { StatsSummary } from '../../api/types'
import { metric } from '../../api/stats-format'
import { useI18n } from '../../i18n'
import { compactMetric, duration } from './format'

const props = defineProps<{ summary: StatsSummary | null; loading?: boolean }>()
const { t, locale } = useI18n()
const labels = computed(() => locale.value === 'zh-CN'
	? { overview: '使用概览', successful: '成功请求', scope: '所选时间范围内', input: '输入', output: '输出', cached: '缓存读取 Token', latency: '平均耗时', latencyHint: '已采集的耗时样本' }
	: { overview: 'Usage overview', successful: 'Successful requests', scope: 'Within the selected period', input: 'Input', output: 'Output', cached: 'Cached input tokens', latency: 'Average latency', latencyHint: 'Recorded latency samples' })
const cards = computed(() => [
	{ key: 'requests', label: labels.value.successful, icon: Activity, value: compactMetric(props.summary?.success_count), precise: metric(props.summary?.success_count), detail: labels.value.scope, tone: 'blue' },
	{ key: 'tokens', label: t('stats.totalTokens'), icon: FileText, value: compactMetric(props.summary?.total_tokens), precise: metric(props.summary?.total_tokens), detail: `${labels.value.input} ${compactMetric(props.summary?.input_tokens)} / ${labels.value.output} ${compactMetric(props.summary?.output_tokens)}`, tone: 'green' },
	{ key: 'cached', label: t('stats.cachedTokens'), icon: Database, value: compactMetric(props.summary?.cached_tokens), precise: metric(props.summary?.cached_tokens), detail: labels.value.cached, tone: 'orange' },
	{ key: 'latency', label: labels.value.latency, icon: Timer, value: duration(props.summary?.avg_latency_ms), precise: props.summary?.avg_latency_ms == null ? '—' : `${metric(props.summary.avg_latency_ms, 2)} ms`, detail: labels.value.latencyHint, tone: 'cyan' },
])
</script>

<template>
	<section class="usage-summary" :aria-label="labels.overview" :aria-busy="loading">
		<article v-for="card in cards" :key="card.key" class="card usage-summary-card">
			<span class="summary-icon" :class="`summary-icon-${card.tone}`"><component :is="card.icon" :size="18" aria-hidden="true" /></span>
			<div class="summary-content">
				<span class="summary-label">{{ card.label }}</span>
				<strong class="summary-value" :title="card.precise">{{ card.value }}</strong>
				<span class="summary-detail" :title="card.detail">{{ card.detail }}</span>
			</div>
		</article>
	</section>
</template>

<style scoped>
.usage-summary { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; }
.usage-summary-card { display: grid; min-height: 92px; grid-template-columns: 36px minmax(0, 1fr); align-items: stretch; gap: 12px; padding: 16px; }
.summary-icon { display: inline-flex; width: 36px; height: 36px; align-items: center; justify-content: center; border-radius: var(--radius-control); }
.summary-icon-blue { background: var(--color-blue-soft, var(--color-info-soft)); color: var(--color-blue-on, var(--color-info)); }
.summary-icon-green { background: var(--color-success-soft); color: var(--color-green-on, var(--color-success)); }
.summary-icon-orange { background: var(--color-warn-soft); color: var(--color-orange-on, var(--color-warn)); }
.summary-icon-cyan { background: var(--color-cyan-soft, var(--color-info-soft)); color: var(--color-cyan-on, var(--color-info)); }
.summary-content { display: flex; min-width: 0; flex-direction: column; justify-content: space-between; gap: 7px; padding: 2px 0; }
.summary-label { color: var(--color-ink-faint); font-size: 12px; font-weight: 700; line-height: 1; }
.summary-value { overflow: hidden; color: var(--color-ink); font-size: 22px; font-weight: 800; line-height: 1; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
.summary-detail { overflow: hidden; color: var(--color-ink-muted); font-size: 12px; font-weight: 600; line-height: 1; text-overflow: ellipsis; white-space: nowrap; }
@media (min-width: 768px) { .usage-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (min-width: 1280px) { .usage-summary { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
</style>
