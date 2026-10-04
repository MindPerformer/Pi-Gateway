<script setup lang="ts">
import { computed, type FunctionalComponent } from 'vue'

const props = withDefaults(defineProps<{
	label: string
	value: string
	firstLabel: string
	firstValue: string
	secondLabel: string
	secondValue: string
	icon: FunctionalComponent
	tone?: 'info' | 'success' | 'warn' | 'danger'
	sparkline?: Array<number | null>
}>(), { tone: 'info', sparkline: () => [] })
const points = computed(() => {
	const samples = props.sparkline.filter((value): value is number => value != null && Number.isFinite(value))
	if (samples.length < 2) return ''
	const min = Math.min(...samples)
	const span = Math.max(...samples) - min
	let previousPresent = false
	return props.sparkline.map((value, index) => {
		if (value == null || !Number.isFinite(value)) { previousPresent = false; return '' }
		const command = previousPresent ? 'L' : 'M'
		previousPresent = true
		return `${command}${index * 180 / (props.sparkline.length - 1)},${span ? 56 - (value - min) / span * 48 : 32}`
	}).join(' ')
})
</script>

<template>
	<article class="card metric-card">
		<div class="metric-top"><span class="metric-icon" :class="`tone-${tone}`"><component :is="icon" :size="18" /></span><span class="metric-label">{{ label }}</span></div>
		<strong class="metric-value">{{ value }}</strong>
		<svg v-if="points" class="metric-sparkline" :class="`sparkline-${tone}`" viewBox="0 0 180 64" preserveAspectRatio="none" aria-hidden="true"><path :d="points" fill="none" stroke="currentColor" stroke-width="1.8" vector-effect="non-scaling-stroke" /></svg>
		<div class="metric-details"><span><small>{{ firstLabel }}</small><b>{{ firstValue }}</b></span><span><small>{{ secondLabel }}</small><b>{{ secondValue }}</b></span></div>
	</article>
</template>

<style scoped>
.metric-card { position: relative; height: 154px; padding: 16px; overflow: hidden; }
.metric-top { display: flex; align-items: center; gap: 10px; }
.metric-icon { width: 34px; height: 34px; flex-shrink: 0; display: inline-flex; align-items: center; justify-content: center; border-radius: 10px; }
.metric-label { color: var(--color-ink-muted); font-size: 13px; font-weight: 650; }
.metric-value { display: block; margin-top: 13px; font-family: var(--font-mono); font-size: 28px; line-height: 1.05; font-weight: 800; letter-spacing: -.04em; font-variant-numeric: tabular-nums; }
.metric-sparkline { position: absolute; top: 26px; right: 15px; width: 42%; height: 62px; pointer-events: none; opacity: .95; }
.sparkline-info { color: var(--color-chart-blue); }
.sparkline-success { color: var(--color-chart-green); }
.sparkline-warn { color: var(--color-chart-orange); }
.sparkline-danger { color: var(--color-danger); }
.metric-details { display: grid; height: 30px; align-items: center; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-top: 12px; padding: 0 12px; border-radius: 9px; background: var(--color-surface-2); }
.metric-details span { min-width: 0; display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.metric-details small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-ink-faint); font-size: 10px; }
.metric-details b { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-ink-muted); font-family: var(--font-mono); font-size: 11px; font-weight: 700; font-variant-numeric: tabular-nums; }
.tone-info { color: var(--color-info); background: var(--color-info-soft); }
.tone-success { color: var(--color-success); background: var(--color-success-soft); }
.tone-warn { color: var(--color-warn); background: var(--color-warn-soft); }
.tone-danger { color: var(--color-danger); background: var(--color-danger-soft); }
</style>
