<script setup lang="ts">
import type { SavedProxy } from '../../api/types'
import { useI18n } from '../../i18n'

defineProps<{ id: string; proxies: SavedProxy[]; loaded: boolean; error: string; keep?: boolean; currentDisplay?: string }>()
const mode = defineModel<string>('mode', { required: true })
const url = defineModel<string>('url', { required: true })
const { t } = useI18n()
</script>

<template>
	<div class="proxy-field">
		<select :id="id" v-model="mode" class="input" :aria-label="t('accounts.col.proxy')">
			<option v-if="keep" value="keep">{{ t('accounts.proxy.keep') }}</option>
			<option value="default">{{ t('accounts.proxy.default') }}</option>
			<option value="manual">{{ t('accounts.proxy.manual') }}</option>
			<optgroup :label="t('accounts.proxy.saved')" :disabled="!loaded"><option v-for="proxy in proxies" :key="proxy.id" :value="`proxy:${proxy.id}`">{{ proxy.name }} · {{ proxy.url }}</option></optgroup>
		</select>
		<input v-if="mode === 'manual'" v-model="url" class="input font-mono" :aria-label="t('accounts.edit.proxy')" :placeholder="t('accounts.edit.proxyPlaceholder')" autocomplete="off" />
		<span v-if="mode === 'keep' && currentDisplay" class="proxy-current">{{ currentDisplay }}</span>
		<p v-if="mode === 'manual' && keep" class="field-hint">{{ t('accounts.proxy.replaceHint') }}</p>
		<p v-if="error" class="proxy-error" role="alert">{{ error }}</p>
	</div>
</template>

<style scoped>
.proxy-field { display: grid; gap: 8px; min-width: 0; width: 100%; }
.proxy-current { color: var(--color-ink-faint); font-family: var(--font-mono); font-size: 11px; overflow-wrap: anywhere; }
.proxy-error { color: var(--color-danger); font-size: 11px; }
.field-hint { margin-top: 0; }
</style>
