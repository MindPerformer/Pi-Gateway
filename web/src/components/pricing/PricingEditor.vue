<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import Modal from '../Modal.vue'
import PriceRates from './PriceRates.vue'
import { emptyPrice, emptyRates, pricing, priceTiers, rateFields, type ModelPrice, type ModelView } from '../../api/pricing'
import type { BillingDetails } from '../../api/types'
import { useI18n } from '../../i18n'
import { ApiError } from '../../api/client'
const props = defineProps<{open: boolean; view: ModelView | null; copy: boolean; revision: number}>()
const emit = defineEmits<{close: []; refresh: []; saved: [model: string, recalculate: boolean]}>()
const {t} = useI18n()
const form = ref<ModelPrice>(emptyPrice())
const aliases = ref('')
const error = ref('')
const busy = ref(false)
const billing = ref<BillingDetails | null>(null)
const tokens = ref({input_tokens: 1000000, cached_tokens: 0, cache_write_tokens: 0, output_tokens: 0})
const tier = ref('standard')
const tokenFields = [{key: 'input_tokens', label: 'input'}, {key: 'cached_tokens', label: 'cached_input'}, {key: 'cache_write_tokens', label: 'cache_write'}, {key: 'output_tokens', label: 'output'}] as const
const tiers = computed(() => priceTiers.filter(value => !value.startsWith('long_') || form.value.context_mode === 'custom'))
watch(() => props.open, open => {
 if (!open) return
 form.value = JSON.parse(JSON.stringify(props.view?.effective ?? emptyPrice(props.view?.model)))
 form.value.disabled = false
 if (props.copy) { form.value.model = ''; form.value.aliases = []; delete form.value.source; delete form.value.source_id; delete form.value.source_url }
 aliases.value = (form.value.aliases ?? []).join('\n')
 billing.value = null; error.value = ''
})
watch([form, tokens, tier], () => { billing.value = null }, {deep: true})
function payload() {
 const value = JSON.parse(JSON.stringify(form.value)) as ModelPrice
 value.model = value.model.trim(); value.aliases = aliases.value.split('\n').map(v => v.trim()).filter(Boolean)
 return value
}
function valid(value: ModelPrice) {
 return value.model && Number.isSafeInteger(value.context_threshold) && value.context_threshold >= 0 && priceTiers.every(k => !value[k] || rateFields.every(f => value[k]![f] === '' || /^\d+(\.\d+)?$/.test(value[k]![f])))
}
function failure(e: unknown) { error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error') }
async function save(recalculate = false) {
 if (busy.value) return
 const value = payload()
 if (!valid(value)) {error.value = t('pricing.invalid'); return}
 busy.value = true; error.value = ''
 try { await pricing.save(props.revision, value); emit('saved', value.model, recalculate) } catch(e) {failure(e)} finally {busy.value = false}
}
async function preview() {
 if (!valid(payload())) {error.value = t('pricing.invalid'); return}
 busy.value = true; error.value = ''
 try {billing.value = (await pricing.preview({model: form.value.model, price: payload(), tier: tier.value, ...tokens.value})).billing} catch(e) {failure(e)} finally {busy.value = false}
}
</script>
<template>
 <Modal :open="open" :title="t(copy ? 'pricing.copy' : 'pricing.edit')" width="max-w-4xl" @close="!busy && emit('close')">
  <form class="pricing-form" @submit.prevent="save()">
   <p v-if="copy" class="pricing-hint">{{ t('pricing.copyHint') }}</p>
   <div class="pricing-grid"><label>{{ t('pricing.model') }}<input v-model="form.model" class="input" required :readonly="!!view && !copy" /></label><label>{{ t('pricing.name') }}<input v-model="form.name" class="input" /></label></div>
   <label>{{ t('pricing.aliases') }}<textarea v-model="aliases" class="input" rows="2" /></label><p class="pricing-hint">{{ t('pricing.aliasHint') }}</p>
   <p class="pricing-hint">{{ t('pricing.rateHint') }}</p>
   <fieldset v-for="key in tiers" :key="key" class="pricing-fieldset"><legend>{{ t(`pricing.${key}`) }}</legend>
    <label class="pricing-check"><input type="checkbox" :checked="!!form[key]" @change="form[key] = ($event.target as HTMLInputElement).checked ? emptyRates() : null" />{{ t('pricing.enableTier') }}</label>
    <div v-if="form[key]" class="pricing-rate-grid"><label v-for="field in rateFields" :key="field">{{ t(`pricing.${field}`) }}<input v-model="form[key]![field]" class="input" inputmode="decimal" :aria-label="`${t(`pricing.${key}`)} · ${t(`pricing.${field}`)}`" pattern="[0-9]+(\.[0-9]+)?|" /></label></div>
   </fieldset>
   <div class="pricing-grid"><label>{{ t('pricing.contextMode') }}<select v-model="form.context_mode" class="input"><option value="same">{{ t('pricing.same') }}</option><option value="custom">{{ t('pricing.custom') }}</option><option value="unpriced">{{ t('pricing.contextUnpriced') }}</option></select></label><label>{{ t('pricing.threshold') }}<input v-model.number="form.context_threshold" class="input" type="number" min="0" step="1" /></label><label>{{ t('pricing.comparison') }}<select v-model="form.context_comparison" class="input"><option value="gt">{{ t('pricing.gt') }}</option><option value="gte">{{ t('pricing.gte') }}</option></select></label></div>
   <label>{{ t('pricing.note') }}<textarea v-model="form.note" class="input" rows="2" /></label>
   <details v-if="view"><summary>{{ t('pricing.snapshot') }}</summary><template v-for="(snapshot, index) in [view.builtin, view.external, view.manual]" :key="index"><div v-if="snapshot" class="pricing-snapshot"><strong>{{ snapshot.source_id || snapshot.source || t(index === 2 ? 'pricing.manual' : 'pricing.builtin') }}</strong><p>{{ snapshot.source_url }}</p><PriceRates :rates="snapshot.standard" /><pre>{{ JSON.stringify(snapshot, null, 2) }}</pre></div></template><p v-if="!view.builtin && !view.external && !view.manual">{{ t('pricing.noSnapshot') }}</p></details>
   <fieldset class="pricing-fieldset"><legend>{{ t('pricing.preview') }}</legend><div class="pricing-rate-grid"><label v-for="field in tokenFields" :key="field.key">{{ t(`pricing.${field.label}`) }}<input v-model.number="tokens[field.key]" class="input" type="number" min="0" step="1" :aria-label="`${t('pricing.tokens')} · ${t(`pricing.${field.label}`)}`" /></label></div><div class="pricing-toolbar"><label>{{ t('pricing.tier') }}<select v-model="tier" class="input"><option value="standard">{{ t('pricing.standard') }}</option><option value="flex">Flex</option><option value="fast">Fast</option></select></label><button type="button" class="btn" :disabled="busy" @click="preview">{{ t('pricing.estimate') }}</button></div><output v-if="billing" aria-live="polite">{{ t('pricing.total') }}: {{ billing.total_cost_micros == null ? t('pricing.unavailable') : `$${billing.total_cost_micros / 1000000}` }}<span v-if="billing.unavailable_reason"> · {{ billing.unavailable_reason }}</span></output></fieldset>
   <div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button type="button" class="btn btn-sm" :disabled="busy" @click="emit('refresh')">{{ t('pricing.refresh') }}</button></div>
  </form>
  <template #footer><button class="btn" :disabled="busy" @click="emit('close')">{{ t('pricing.cancel') }}</button><button class="btn" :disabled="busy" @click="save(true)">{{ t('pricing.saveRecalculate') }}</button><button class="btn btn-primary" :disabled="busy" @click="save()">{{ t('pricing.save') }}</button></template>
 </Modal>
</template>
