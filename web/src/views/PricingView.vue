<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import Modal from '../components/Modal.vue'
import PricingEditor from '../components/pricing/PricingEditor.vue'
import PricingSources from '../components/pricing/PricingSources.vue'
import PricingHistory from '../components/pricing/PricingHistory.vue'
import PricingSyncPreview from '../components/pricing/PricingSyncPreview.vue'
import PricingRecalculate from '../components/pricing/PricingRecalculate.vue'
import PriceRates from '../components/pricing/PriceRates.vue'
import { pricing, type ModelView, type PriceSource, type SyncRun, type HistoryEntry, type StandardPriceFile } from '../api/pricing'
import { ApiError } from '../api/client'
import { useI18n } from '../i18n'
import { useToastStore } from '../stores/ui'
const {t} = useI18n()
const toast = useToastStore()
const tab = ref('catalog')
const tabs = ['catalog', 'sources', 'history'] as const
function tabKey(event: KeyboardEvent, index: number) {
 let next = index
 if (event.key === 'ArrowRight') next = (index + 1) % tabs.length
 else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length
 else if (event.key === 'Home') next = 0
 else if (event.key === 'End') next = tabs.length - 1
 else return
 event.preventDefault(); tab.value = tabs[next]!
 document.getElementById(`pricing-tab-${tab.value}`)?.focus()
}
const revision = ref(0)
const models = ref<ModelView[]>([])
const sources = ref<PriceSource[]>([])
const runs = ref<SyncRun[]>([])
const history = ref<HistoryEntry[]>([])
const loading = ref(false)
const error = ref('')
const search = ref('')
const status = ref('')
const source = ref('')
const onlyUnpriced = ref(false)
const page = ref(1)
const pageSize = 30
const filtered = computed(() => models.value.filter(row => {
 const needle = search.value.trim().toLowerCase()
 return (!needle || [row.model, row.name, row.source, row.effective?.source_id, row.effective?.source_url, ...(row.effective?.aliases ?? [])].some(v => v?.toLowerCase().includes(needle))) && (!status.value || row.status === status.value) && (!source.value || row.source === source.value || row.effective?.source_id === source.value || (source.value === 'manual' && !!row.manual)) && (!onlyUnpriced.value || row.status === 'unpriced')
}))
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize)))
const visible = computed(() => filtered.value.slice((Math.min(page.value, pages.value) - 1) * pageSize, Math.min(page.value, pages.value) * pageSize))
const sourceOptions = computed(() => [...new Set(models.value.flatMap(row => [row.source, row.effective?.source_id ?? '']).filter(Boolean))])
let sequence = 0
async function load() {
 const seq = ++sequence
 loading.value = true; error.value = ''
 try {
  const [catalog, external, sync, changes] = await Promise.all([pricing.models(), pricing.sources(), pricing.runs(), pricing.history()])
  if (seq !== sequence) return
  // Never combine a newer catalog with stale write expectations from another list.
  models.value = catalog.items ?? []; sources.value = external.items ?? []; runs.value = sync.items ?? []; history.value = changes.items ?? []; revision.value = catalog.revision
 } catch(e) {if (seq === sequence) failure(e)} finally {if (seq === sequence) loading.value = false}
}
function failure(e: unknown) {error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')}
onMounted(load)
const editorOpen = ref(false)
const editing = ref<ModelView | null>(null)
const copying = ref(false)
function edit(row: ModelView | null = null, copy = false) {editing.value = row; copying.value = copy; editorOpen.value = true}
const recalculateOpen = ref(false)
const recalculateModel = ref('')
function recalculate(model = '') {recalculateModel.value = model; recalculateOpen.value = true}
async function saved(model: string, calculate: boolean) {editorOpen.value = false; toast.success(t('pricing.saved')); await load(); if (calculate) recalculate(model)}
const preview = ref<SyncRun | null>(null)
async function applied() {preview.value = null; toast.success(t('pricing.applied')); await load()}
const fileInput = ref<HTMLInputElement>()
async function importFile(event: Event) {
 const input = event.target as HTMLInputElement
 const file = input.files?.[0]
 if (!file) return
 loading.value = true; error.value = ''
 try {const document = JSON.parse(await file.text()) as StandardPriceFile; const result = await pricing.import(revision.value, document); preview.value = result.run; await load()} catch(e) {failure(e)} finally {loading.value = false; input.value = ''}
}
async function exportFile() {
 error.value = ''
 try {const document = await pricing.export(); const url = URL.createObjectURL(new Blob([JSON.stringify(document, null, 2)], {type: 'application/json'})); const anchor = window.document.createElement('a'); anchor.href = url; anchor.download = 'model-prices.json'; anchor.click(); URL.revokeObjectURL(url)} catch(e) {failure(e)}
}
const action = ref<{kind: 'reset' | 'block' | 'follow'; row: ModelView} | null>(null)
const following = ref('')
const actionBusy = ref(false)
const actionError = ref('')
function requestAction(kind: 'reset' | 'block' | 'follow', row: ModelView) {action.value = {kind, row}; following.value = row.following_source_id || sources.value[0]?.id || ''; actionError.value = ''}
async function confirmAction() {
 if (!action.value || actionBusy.value) return
 actionBusy.value = true; actionError.value = ''
 try {const {kind, row} = action.value; if (kind === 'follow') await pricing.follow(revision.value, row.model, following.value); else await pricing[kind](revision.value, row.model); action.value = null; await load()} catch(e) {actionError.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')} finally {actionBusy.value = false}
}
</script>
<template>
 <div class="pricing-page page-container">
  <PageHeader :title="t('pricing.title')" :subtitle="t('pricing.subtitle')"><template #actions><span class="pricing-hint">{{ t('pricing.revision') }} {{ revision }}</span><button class="btn" :disabled="loading" @click="load">{{ t('pricing.refresh') }}</button></template></PageHeader>
  <div class="pricing-tabs" role="tablist" :aria-label="t('pricing.title')"><button v-for="(item, index) in tabs" :id="`pricing-tab-${item}`" :key="item" class="btn" :class="{'btn-primary': tab === item}" role="tab" :aria-selected="tab === item" :aria-controls="`pricing-panel-${item}`" :tabindex="tab === item ? 0 : -1" @keydown="tabKey($event, index)" @click="tab = item">{{ t(`pricing.${item}`) }}</button></div>
  <p v-if="error" role="alert" class="pricing-error">{{ error }}</p><output v-if="loading" aria-live="polite" class="pricing-hint">{{ t('pricing.loading') }}</output>
  <section v-if="tab === 'catalog'" id="pricing-panel-catalog" role="tabpanel" aria-labelledby="pricing-tab-catalog" class="pricing-form">
   <div class="pricing-toolbar"><button class="btn btn-primary" :disabled="loading" @click="edit()">{{ t('pricing.new') }}</button><button class="btn" :disabled="loading" @click="exportFile">{{ t('pricing.export') }}</button><button class="btn" :disabled="loading" @click="fileInput?.click()">{{ t('pricing.import') }}</button><input ref="fileInput" type="file" accept=".json,application/json" class="sr-only" :aria-label="t('pricing.import')" @change="importFile" /><button class="btn" :disabled="loading" @click="recalculate()">{{ t('pricing.recalculate') }}</button></div>
   <div class="pricing-filters"><label>{{ t('pricing.search') }}<input v-model="search" type="search" class="input" @input="page = 1" /></label><label>{{ t('pricing.status') }}<select v-model="status" class="input" @change="page = 1"><option value="">{{ t('pricing.all') }}</option><option v-for="value in ['priced', 'unpriced', 'blocked']" :key="value" :value="value">{{ t(`pricing.${value}`) }}</option></select></label><label>{{ t('pricing.source') }}<select v-model="source" class="input" @change="page = 1"><option value="">{{ t('pricing.all') }}</option><option v-for="value in sourceOptions" :key="value" :value="value">{{ value }}</option></select></label><label class="pricing-check"><input v-model="onlyUnpriced" type="checkbox" @change="page = 1" />{{ t('pricing.onlyUnpriced') }}</label></div>
   <p v-if="!visible.length && !loading">{{ t('pricing.empty') }}</p>
   <article v-for="row in visible" :key="row.model" class="pricing-card"><header class="pricing-toolbar"><strong class="pricing-model">{{ row.model }}</strong><span v-if="row.name">{{ row.name }}</span><span class="pricing-status" :class="`pricing-status-${row.status}`">{{ t(`pricing.${row.status}`) }}</span><span v-if="row.manual" class="pricing-lock">{{ t('pricing.manual') }}</span></header><p class="pricing-hint">{{ t('pricing.source') }}: {{ row.effective?.source_id || row.source || '—' }}<span v-if="row.following_source_id"> · {{ t('pricing.follow') }}: {{ row.following_source_id }}</span></p><PriceRates :rates="row.effective?.standard" /><p v-if="row.effective?.aliases?.length" class="pricing-hint">{{ t('pricing.aliases') }}: {{ row.effective.aliases.join(', ') }}</p><div class="pricing-toolbar"><button class="btn btn-sm" :disabled="loading" @click="edit(row)">{{ t('pricing.edit') }}</button><button class="btn btn-sm" :disabled="loading" @click="edit(row, true)">{{ t('pricing.copy') }}</button><button class="btn btn-sm" :disabled="loading" @click="requestAction('reset', row)">{{ t('pricing.reset') }}</button><button class="btn btn-sm" :disabled="loading || !sources.length" @click="requestAction('follow', row)">{{ t('pricing.follow') }}</button><button class="btn btn-sm" :disabled="loading || row.status === 'blocked'" @click="requestAction('block', row)">{{ t('pricing.block') }}</button><button class="btn btn-sm" :disabled="loading" @click="recalculate(row.model)">{{ t('pricing.recalculate') }}</button></div></article>
   <div v-if="filtered.length > pageSize" class="pricing-toolbar"><button class="btn" :disabled="page <= 1" :aria-label="t('pricing.previous')" @click="page--">←</button><span>{{ Math.min(page, pages) }} / {{ pages }} · {{ filtered.length }}</span><button class="btn" :disabled="page >= pages" :aria-label="t('pricing.next')" @click="page++">→</button></div>
  </section>
  <section v-if="tab === 'sources'" id="pricing-panel-sources" role="tabpanel" aria-labelledby="pricing-tab-sources"><PricingSources :sources="sources" :runs="runs" :revision="revision" :disabled="loading" @changed="load" @preview="preview = $event" /></section>
  <section v-if="tab === 'history'" id="pricing-panel-history" role="tabpanel" aria-labelledby="pricing-tab-history"><PricingHistory :items="history" :revision="revision" :disabled="loading" @changed="load" /></section>
  <PricingEditor :open="editorOpen" :view="editing" :copy="copying" :revision="revision" @close="editorOpen = false" @saved="saved" @refresh="load" />
  <PricingSyncPreview :run="preview" :revision="revision" :disabled="loading" @close="preview = null" @applied="applied" @refresh="load" />
  <PricingRecalculate :open="recalculateOpen" :model="recalculateModel" :revision="revision" @close="recalculateOpen = false" @changed="load" />
  <Modal :open="!!action" :title="action ? t(`pricing.${action.kind}`) : ''" @close="!actionBusy && (action = null)"><div class="pricing-form"><strong>{{ action?.row.model }}</strong><p>{{ action ? t(`pricing.${action.kind}Hint`) : '' }}</p><label v-if="action?.kind === 'follow'">{{ t('pricing.source') }}<select v-model="following" class="input"><option v-for="item in sources" :key="item.id" :value="item.id">{{ item.name }} ({{ item.id }})</option></select></label><div v-if="actionError"><p role="alert" class="pricing-error">{{ actionError }}</p><button class="btn btn-sm" :disabled="actionBusy || loading" @click="load">{{ t('pricing.refresh') }}</button></div></div><template #footer><button class="btn" :disabled="actionBusy" @click="action = null">{{ t('pricing.cancel') }}</button><button class="btn btn-primary" :disabled="actionBusy || (action?.kind === 'follow' && !following)" @click="confirmAction">{{ t('pricing.confirm') }}</button></template></Modal>
 </div>
</template>
<style>
.pricing-page{display:grid;gap:20px;padding:24px;max-width:1500px;margin:0 auto;color:var(--color-ink)}.pricing-tabs,.pricing-toolbar{display:flex;align-items:center;flex-wrap:wrap;gap:10px}.pricing-form{display:grid;gap:14px}.pricing-form label,.pricing-filters>label{display:grid;gap:6px;font-size:12px;color:var(--color-ink-muted)}.pricing-form .pricing-check,.pricing-check{display:flex;align-items:center;gap:7px}.pricing-check input{accent-color:var(--color-accent);width:16px;height:16px}.pricing-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:14px}.pricing-rate-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.pricing-filters{display:grid;grid-template-columns:minmax(160px,2fr) 1fr 1fr auto;align-items:end;gap:12px}.pricing-card,.pricing-fieldset{padding:16px;border:1px solid var(--color-line);border-radius:12px;background:var(--color-surface);min-width:0}.pricing-card{display:grid;gap:12px}.pricing-card p,.pricing-card h4{margin:0}.pricing-fieldset legend{padding:0 6px;font-size:13px;font-weight:650}.pricing-fieldset{display:grid;gap:12px}.pricing-hint{color:var(--color-ink-muted);font-size:12px;line-height:1.6;margin:0}.pricing-error{color:var(--color-danger);font-size:13px;white-space:pre-wrap;overflow-wrap:anywhere}.pricing-warning{color:var(--color-warn);font-size:12px;line-height:1.6}.pricing-lock{color:var(--color-warn);font-size:11px}.pricing-status{font-size:11px;padding:3px 8px;border-radius:6px;background:var(--color-surface-2)}.pricing-status-priced{color:var(--color-success)}.pricing-status-blocked{color:var(--color-danger)}.pricing-model,.pricing-url{overflow-wrap:anywhere}.pricing-form pre,.pricing-card pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:240px;overflow:auto;font-size:11px}.pricing-snapshot{display:grid;gap:8px;padding:10px 0}.pricing-form details summary,.pricing-card summary{cursor:pointer;color:var(--color-ink-muted);font-size:12px}.pricing-form dd,.pricing-card dd{margin:5px 0 0;font-family:var(--font-mono)}.pricing-form dt,.pricing-card dt{font-size:11px;color:var(--color-ink-muted)}
@media(max-width:700px){.pricing-page{padding:18px 12px}.pricing-filters{grid-template-columns:1fr 1fr}.pricing-filters>label:first-child{grid-column:1/-1}.pricing-rate-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.pricing-grid{grid-template-columns:1fr}.pricing-toolbar .btn{max-width:100%;white-space:normal}.pricing-tabs .btn{flex:1;font-size:12px}.pricing-card{padding:12px}}
</style>
