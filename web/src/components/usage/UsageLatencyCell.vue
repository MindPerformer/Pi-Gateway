<script setup lang="ts">
import { computed } from 'vue'
import { metric } from '../../api/stats-format'
import { useI18n } from '../../i18n'
import { duration } from './format'

defineProps<{ firstToken: number | null; latency: number | null; throughput?: number | null }>()
const { locale } = useI18n()
const labels = computed(() => locale.value === 'zh-CN' ? { first: '首字', total: '耗时', throughput: 'TPS 流速', throughputHelp: '同 codex2api：输出 Token ÷ 输出阶段秒数；首字按首个非生命周期响应事件计时（含思考结构帧）。缺少有效首字或输出区间不足 20ms 时用总耗时；失败、未完成或无输出不计流速。' } : { first: 'TTFT', total: 'Total', throughput: 'TPS', throughputHelp: 'Matches codex2api: output tokens / generation seconds. TTFT is the first non-lifecycle response event, including reasoning structure events. Uses total time when TTFT is invalid or the interval is under 20ms; excludes failed, unfinished and empty outputs.' })
</script>

<template>
	<div class="latency-cell">
		<span class="latency-label">{{ labels.first }}</span><span class="latency-first" :title="firstToken == null ? '—' : `${metric(firstToken, 2)} ms`">{{ duration(firstToken) }}</span>
		<span class="latency-label">{{ labels.total }}</span><span :title="latency == null ? '—' : `${metric(latency, 2)} ms`">{{ duration(latency) }}</span>
		<template v-if="throughput !== undefined"><span class="latency-label" :title="labels.throughputHelp">{{ labels.throughput }}</span><span :title="labels.throughputHelp">{{ throughput == null ? '—' : `${metric(throughput, 2)} token/s` }}</span></template>
	</div>
</template>

<style scoped>
.latency-cell { display: inline-grid; grid-template-columns: auto auto; align-items: center; justify-content: end; gap: 6px 8px; color: var(--color-ink); font-family: var(--font-mono); font-size: 12px; font-weight: 750; font-variant-numeric: tabular-nums; line-height: 1; white-space: nowrap; }
.latency-label { color: var(--color-ink-faint); font-size: 11px; }
.latency-first { color: var(--color-ink-muted); }
</style>
