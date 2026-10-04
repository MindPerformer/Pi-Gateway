<script setup lang="ts">
import { nextTick, ref, useId } from 'vue'
import { CalendarDays, X } from 'lucide-vue-next'
import type { StatsRange } from '../api/types'
import { useI18n } from '../i18n'

const props = defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ change: [range: StatsRange] }>()
const { t } = useI18n()
const id = useId()
const preset = ref('24h')
const customOpen = ref(false)
const startElement = ref<HTMLInputElement>()
const localInput = (ms: number) => {
	const date = new Date(ms)
	return new Date(ms - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
const endInput = ref(localInput(Date.now()))
const startInput = ref(localInput(Date.now() - 86400000))
const error = ref('')
const durations: Record<string, number> = { '1h': 3600000, '24h': 86400000, '7d': 604800000, '30d': 2592000000 }
function apply() {
	if (props.disabled) return
	error.value = ''
	const end = preset.value === 'custom' ? new Date(endInput.value).getTime() : Date.now()
	const start = preset.value === 'custom' ? new Date(startInput.value).getTime() : end - durations[preset.value]!
	if (!Number.isFinite(start) || !Number.isFinite(end) || start >= end) {
		error.value = t('stats.invalidRange')
		customOpen.value = true
		return
	}
	if (preset.value !== 'custom') { startInput.value = localInput(start); endInput.value = localInput(end) }
	customOpen.value = false
	emit('change', { start, end })
}
async function select() {
	error.value = ''
	if (preset.value !== 'custom') { apply(); return }
	customOpen.value = true
	await nextTick()
	startElement.value?.focus()
}
defineExpose({ refresh: apply })
</script>

<template>
	<div class="time-range-picker" @keydown.esc="customOpen = false">
		<select v-model="preset" class="input range-select" :aria-label="t('stats.range')" :disabled="props.disabled" @change="select">
			<option v-for="value in ['1h', '24h', '7d', '30d', 'custom']" :key="value" :value="value">{{ t(`stats.range.${value}`) }}</option>
		</select>
		<button v-if="preset === 'custom'" type="button" class="btn btn-ghost btn-icon" :aria-label="t('stats.range.custom')" :aria-expanded="customOpen" :aria-controls="id" :disabled="props.disabled" @click="customOpen = !customOpen"><CalendarDays :size="16" /></button>
		<form v-if="customOpen" :id="id" class="custom-range-panel" :aria-label="t('stats.range.custom')" @submit.prevent="apply">
			<div class="custom-range-heading"><strong>{{ t('stats.range.custom') }}</strong><button type="button" class="btn btn-ghost btn-icon" :aria-label="t('common.close')" @click="customOpen = false"><X :size="14" /></button></div>
			<label><span class="label">{{ t('stats.start') }}</span><input ref="startElement" v-model="startInput" class="input" type="datetime-local" required :disabled="props.disabled" /></label>
			<label><span class="label">{{ t('stats.end') }}</span><input v-model="endInput" class="input" type="datetime-local" required :disabled="props.disabled" /></label>
			<p v-if="error" role="alert" class="range-error">{{ error }}</p>
			<button class="btn btn-primary range-apply" :disabled="props.disabled" type="submit">{{ t('stats.apply') }}</button>
		</form>
	</div>
</template>

<style scoped>
.time-range-picker { position: relative; display: inline-flex; align-items: center; gap: 4px; }
.range-select { width: 136px; }
.custom-range-panel { position: absolute; top: calc(100% + 8px); right: 0; z-index: 35; display: grid; width: min(320px, calc(100vw - 40px)); gap: 12px; padding: 16px; border-radius: var(--radius-card); background: var(--color-surface); box-shadow: var(--shadow-overlay); }
.custom-range-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; color: var(--color-ink); font-size: 14px; }
.custom-range-heading .btn { min-height: 28px; width: 28px; }
.range-error { margin: 0; color: var(--color-danger); font-size: 12px; }
.range-apply { justify-self: end; }
</style>
