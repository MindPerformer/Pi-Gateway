<script setup lang="ts">
import { computed } from 'vue'
import type { UsageRecord } from '../../api/types'
import { useI18n } from '../../i18n'
const props = defineProps<{ record: UsageRecord }>()
const { t } = useI18n()
const effort = computed(() => props.record.reasoning_effort?.trim().toLowerCase() ?? '')
const fast = computed(() => ['fast', 'priority'].includes((props.record.service_tier || props.record.service_priority || '').trim().toLowerCase()))
const levels: Record<string, number> = { none: 0, minimal: 1, low: 2, medium: 3, high: 4, xhigh: 5, max: 6 }
</script>

<template>
	<span class="request-badges">
		<span v-if="effort" class="request-badge" :class="`reasoning-level-${levels[effort] ?? 'unknown'}`" :title="t('usage.reasoningHint', { effort })">{{ effort }}</span>
		<span v-if="fast" class="request-badge fast-badge" :title="t('usage.fastHint')">Fast</span>
	</span>
</template>

<style scoped>
.request-badges { display: inline-flex; flex-wrap: wrap; gap: 5px; }
.request-badges:empty { display: none; }
.request-badge { display: inline-flex; height: 24px; align-items: center; justify-content: center; padding: 0 8px; border-radius: 999px; font-family: var(--font-mono); font-size: 11px; font-weight: 700; line-height: 1; }
.reasoning-level-0, .reasoning-level-unknown { color: var(--color-ink-muted); background: var(--color-surface-2); }
.reasoning-level-1 { color: var(--color-success); background: var(--color-success-soft); }
.reasoning-level-2 { color: var(--color-info); background: var(--color-info-soft); }
.reasoning-level-3 { color: light-dark(#7c3aed, #c4b5fd); background: light-dark(#ede9fe, #7c3aed26); }
.reasoning-level-4 { color: var(--color-warn); background: var(--color-warn-soft); }
.reasoning-level-5 { color: light-dark(#be185d, #f9a8d4); background: light-dark(#fce7f3, #be185d26); }
.reasoning-level-6 { color: var(--color-danger); background: var(--color-danger-soft); box-shadow: inset 0 0 0 1px currentColor; }
.fast-badge { color: var(--color-success); background: var(--color-success-soft); }
</style>
