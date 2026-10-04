<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from '../i18n'
const { t } = useI18n()

const props = defineProps<{
	modelValue: boolean
	label?: string
	disabled?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [boolean] }>()

const classes = computed(() => (props.modelValue ? 'toggle-track-on' : 'toggle-track-off'))
</script>

<template>
	<button
		type="button"
		role="switch"
		:aria-checked="props.modelValue"
		:aria-label="props.label || t(props.modelValue ? 'common.enabled' : 'common.disabled')"
		class="toggle-control inline-flex shrink-0 items-center gap-2 rounded-full disabled:opacity-60"
		:disabled="props.disabled"
		@click="emit('update:modelValue', !props.modelValue)"
	>
		<span class="toggle-track" :class="classes">
			<span class="toggle-thumb" :class="props.modelValue ? 'toggle-thumb-on' : 'toggle-thumb-off'" />
		</span>
		<span v-if="props.label" class="toggle-label">{{ props.label }}</span>
	</button>
</template>

<style scoped>
/* Dimensions and states from the deployed @codex-proxy/ui BaseSwitch. */
.toggle-track { position: relative; display: inline-flex; height: 24px; min-width: 44px; flex-shrink: 0; align-items: center; padding: 2px; border-radius: 999px; box-shadow: var(--cp-box-shadow-tertiary); transition: background-color 180ms, box-shadow 180ms; }
.toggle-track-on { background: var(--cp-color-primary); }
.toggle-track-off { background: var(--cp-switch-unchecked-bg); }
.toggle-thumb { position: absolute; top: 2px; width: 20px; height: 20px; border-radius: 999px; background: var(--cp-color-white); box-shadow: var(--cp-box-shadow-tertiary); transition: left 180ms ease-out; }
.toggle-thumb-on { left: calc(100% - 22px); }
.toggle-thumb-off { left: 2px; }
.toggle-label { color: var(--cp-color-text); font-size: var(--cp-font-size); font-weight: 650; }
.toggle-control:disabled { cursor: not-allowed; }
.toggle-control:disabled .toggle-track { background: var(--cp-color-bg-container-disabled); box-shadow: none; }
</style>
