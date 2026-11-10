<script setup lang="ts">
import { ref, watch } from 'vue'
import Modal from '../Modal.vue'
import { pricing, type RecalculatePreview, type RecalculateQuery, type RecalculateResult } from '../../api/pricing'
import { ApiError } from '../../api/client'
import { useI18n } from '../../i18n'
const props = defineProps<{open: boolean; model: string; revision: number}>()
const emit = defineEmits<{close: []; changed: []}>()
const {t} = useI18n()
const model = ref('')
const account = ref<number | ''>('')
const start = ref('')
const end = ref('')
const limit = ref(500)
const preview = ref<RecalculatePreview | null>(null)
const result = ref<RecalculateResult | null>(null)
const query = ref<RecalculateQuery | null>(null)
const busy = ref(false)
const error = ref('')
watch(() => props.open, open => {if (open) {model.value = props.model; account.value = ''; start.value = ''; end.value = ''; preview.value = null; result.value = null; query.value = null; error.value = ''}})
watch([model, account, start, end, limit], () => {preview.value = null; result.value = null; query.value = null})
function failure(e: unknown) {error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')}
async function load(next = false) {
 if (busy.value) return
 const previous = query.value
 const body: RecalculateQuery = next && previous ? {...previous, after_id: result.value?.next_after_id ?? preview.value?.next_after_id} : {expected_revision: props.revision, limit: limit.value}
 delete body.preview_token
 if (!next) {if (model.value.trim()) body.model = model.value.trim(); if (account.value) body.account_id = Number(account.value); if (start.value) body.start_at = new Date(start.value).getTime(); if (end.value) body.end_at = new Date(end.value).getTime()}
 if (body.start_at && body.end_at && body.start_at >= body.end_at) {error.value = t('pricing.invalid'); return}
 busy.value = true; error.value = ''; preview.value = null; result.value = null
 try {preview.value = await pricing.recalculatePreview(body); query.value = body} catch(e) {failure(e)} finally {busy.value = false}
}
async function apply() {
 if (!preview.value || !query.value || busy.value) return
 busy.value = true; error.value = ''
 try {result.value = await pricing.recalculate({...query.value, preview_token: preview.value.preview_token}); emit('changed')} catch(e) {failure(e); preview.value = null} finally {busy.value = false}
}
const dollars = (micros: number) => `$${(micros / 1000000).toFixed(6)}`
</script>
<template><Modal :open="open" :title="t('pricing.recalculate')" width="max-w-3xl" @close="!busy && emit('close')"><div class="pricing-form"><p class="pricing-warning">{{ t('pricing.recalculateHint') }}</p><form class="pricing-form" @submit.prevent="load()"><div class="pricing-grid"><label>{{ t('pricing.model') }}<input v-model="model" class="input" /></label><label>{{ t('pricing.account') }}<input v-model="account" class="input" type="number" min="1" step="1" /></label><label>{{ t('pricing.start') }}<input v-model="start" class="input" type="datetime-local" /></label><label>{{ t('pricing.end') }}<input v-model="end" class="input" type="datetime-local" /></label><label>{{ t('pricing.batch') }}<input v-model.number="limit" class="input" type="number" min="1" max="2000" required /></label></div><button class="btn" :disabled="busy">{{ t('pricing.previewRecalculate') }}</button></form>
 <section v-if="preview" class="pricing-card" aria-live="polite"><dl class="pricing-grid"><div><dt>{{ t('pricing.matched') }}</dt><dd>{{ preview.matched }}</dd></div><div><dt>{{ t('pricing.eligible') }}</dt><dd>{{ preview.eligible }}</dd></div><div><dt>{{ t('pricing.skipped') }}</dt><dd>{{ preview.already_repriced }}</dd></div><div><dt>{{ t('pricing.cannot') }}</dt><dd>{{ preview.missing_usage + preview.unpriced }}</dd></div><div><dt>{{ t('pricing.amount') }}</dt><dd>{{ dollars(preview.cost_micros) }}</dd></div></dl><p v-if="preview.has_more" class="pricing-hint">{{ t('pricing.moreHint') }}</p><details v-if="preview.items?.length"><summary>{{ t('pricing.candidates') }} ({{ preview.items.length }})</summary><ul><li v-for="item in preview.items" :key="item.id">#{{ item.id }} · {{ item.model }} · {{ item.account_id }} · {{ dollars(item.cost_micros) }} · {{ item.price_version }}</li></ul></details></section>
 <output v-if="result" aria-live="polite">{{ t('pricing.recalculated', {updated: result.updated, skipped: result.skipped}) }}</output><div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button class="btn btn-sm" :disabled="busy" @click="emit('changed')">{{ t('pricing.refresh') }}</button></div></div><template #footer><button class="btn" :disabled="busy" @click="emit('close')">{{ t('pricing.cancel') }}</button><button v-if="result?.has_more" class="btn" :disabled="busy" @click="load(true)">{{ t('pricing.nextBatch') }}</button><button v-if="preview && !result" class="btn btn-primary" :disabled="busy || !preview.eligible" @click="apply">{{ t('pricing.applyRecalculate') }}</button></template></Modal></template>
