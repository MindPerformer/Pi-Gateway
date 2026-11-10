<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import Modal from '../Modal.vue'
import PriceRates from './PriceRates.vue'
import { pricing, type SyncRun } from '../../api/pricing'
import { ApiError } from '../../api/client'
import { useI18n } from '../../i18n'
const props = defineProps<{run: SyncRun | null; revision: number; disabled: boolean}>()
const emit = defineEmits<{close: []; applied: []; refresh: []}>()
const {t} = useI18n()
const selected = ref<string[]>([])
const busy = ref(false)
const error = ref('')
watch(() => props.run, run => { selected.value = (run?.items ?? []).filter(item => !item.locked && item.selected === true).map(item => item.model); error.value = '' })
const selectable = computed(() => (props.run?.items ?? []).filter(item => !item.locked))
const all = computed(() => selectable.value.length > 0 && selectable.value.every(item => selected.value.includes(item.model)))
function toggleAll(checked: boolean) {selected.value = checked ? selectable.value.map(item => item.model) : []}
async function apply() {
 if (!props.run || !selected.value.length || busy.value || props.disabled) return
 busy.value = true; error.value = ''
 try {await pricing.apply(props.revision, props.run.id, selected.value); emit('applied')} catch(e) {error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')} finally {busy.value = false}
}
</script>
<template><Modal :open="!!run" :title="t('pricing.syncPreview')" width="max-w-4xl" @close="!busy && emit('close')">
 <div class="pricing-form"><p class="pricing-hint">{{ t('pricing.syncHint') }}</p><p>{{ run?.source_id }} · {{ run?.status }} · {{ t('pricing.revision') }} {{ run?.base_revision }}</p><p v-if="run?.error" role="alert" class="pricing-error">{{ run.error }}</p>
 <label class="pricing-check"><input type="checkbox" :checked="all" :disabled="!selectable.length || busy" @change="toggleAll(($event.target as HTMLInputElement).checked)" />{{ t('pricing.selectAll') }}</label>
 <article v-for="item in run?.items" :key="item.model" class="pricing-card"><div class="pricing-toolbar"><label class="pricing-check"><input v-model="selected" type="checkbox" :value="item.model" :disabled="item.locked || busy" :aria-label="`${t('pricing.select')} ${item.model}`" /><strong>{{ item.model }}</strong></label><span v-if="item.locked" class="pricing-lock">{{ t('pricing.locked') }}</span></div><div class="pricing-grid"><div><h4>{{ t('pricing.before') }}</h4><PriceRates :rates="item.before?.standard" /><details v-if="item.before"><summary>{{ t('pricing.snapshot') }}</summary><pre>{{ JSON.stringify(item.before, null, 2) }}</pre></details></div><div><h4>{{ t('pricing.after') }}</h4><PriceRates :rates="item.after.standard" /><details><summary>{{ t('pricing.snapshot') }}</summary><pre>{{ JSON.stringify(item.after, null, 2) }}</pre></details></div></div><ul v-if="item.warnings?.length" class="pricing-warning" :aria-label="t('pricing.warnings')"><li v-for="warning in item.warnings" :key="warning">{{ warning }}</li></ul></article>
 <p v-if="!run?.items?.length">{{ t('pricing.empty') }}</p><div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button class="btn btn-sm" :disabled="busy || disabled" @click="emit('refresh')">{{ t('pricing.refresh') }}</button></div></div>
 <template #footer><button class="btn" :disabled="busy" @click="emit('close')">{{ t('pricing.cancel') }}</button><button class="btn btn-primary" :disabled="busy || disabled || !selected.length" @click="apply">{{ t('pricing.apply') }} ({{ selected.length }})</button></template>
 </Modal></template>
