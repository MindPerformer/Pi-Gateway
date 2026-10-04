<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
import { useI18n } from '../i18n'
const { t } = useI18n()

const props = withDefaults(
	defineProps<{
		value: unknown
		label?: string
		maxHeight?: string
		// raw renders a plain string as-is; otherwise JSON is pretty printed.
		raw?: boolean
	}>(),
	{ maxHeight: '24rem', raw: false },
)

const copied = ref(false)

const text = computed(() => {
	if (props.value === null || props.value === undefined) return ''
	if (props.raw) return String(props.value)
	if (typeof props.value === 'string') {
		// A JSON string payload is prettified when possible.
		try {
			return JSON.stringify(JSON.parse(props.value), null, 2)
		} catch {
			return props.value
		}
	}
	try {
		return JSON.stringify(props.value, null, 2)
	} catch {
		return String(props.value)
	}
})

async function copy() {
	try {
		await navigator.clipboard.writeText(text.value)
		copied.value = true
		setTimeout(() => (copied.value = false), 1500)
	} catch {
		// Clipboard access can be blocked; the user can still select the text.
	}
}
</script>

<template>
	<div class="code-panel">
		<div v-if="label" class="code-header">
			<span>{{ label }}</span>
			<button class="btn btn-ghost btn-icon" :aria-label="t('common.copy')" :title="t('common.copy')" @click="copy">
				<component :is="copied ? Check : Copy" class="h-3.5 w-3.5" />
			</button>
		</div>
		<pre
			class="code-content"
			:style="{ maxHeight: props.maxHeight }"
			>{{ text || '—' }}</pre
		>
	</div>
</template>
