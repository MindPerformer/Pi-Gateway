<script setup lang="ts">
import { ref, watch } from 'vue'
import { pricing, type PriceSource, type SyncRun } from '../../api/pricing'
import { ApiError } from '../../api/client'
import Modal from '../Modal.vue'
import { formatDateTime, useI18n } from '../../i18n'
const props = defineProps<{sources: PriceSource[]; runs: SyncRun[]; revision: number; disabled: boolean}>()
const emit = defineEmits<{changed: []; preview: [run: SyncRun]}>()
const {t} = useI18n()
const open = ref(false)
const editing = ref(false)
const form = ref<PriceSource>(newSource())
const hosts = ref('')
const busy = ref(false)
const error = ref('')
const result = ref('')
const deleting = ref<PriceSource | null>(null)
function newSource(): PriceSource {return {id: '', name: '', url: '', format: 'standard', enabled: true, interval_hours: 24, auto_apply: false, trusted_hosts: []}}
function edit(source?: PriceSource) {form.value = JSON.parse(JSON.stringify(source ?? newSource())); hosts.value = (source?.trusted_hosts ?? []).join('\n'); editing.value = !!source; error.value = ''; result.value = ''; open.value = true}
watch(() => form.value.format, format => {if (format === 'models_dev' && (!form.value.url || form.value.url === 'https://models.dev/api.json')) form.value.url = 'https://models.dev/api.json'})
function failure(e: unknown) {error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')}
async function save() {
 if (busy.value) return
 busy.value = true; error.value = ''
 try {const source = {...form.value, trusted_hosts: hosts.value.split('\n').map(v => v.trim()).filter(Boolean)}; if (!source.proxy_id) delete source.proxy_id; await pricing.saveSource(props.revision, source); open.value = false; emit('changed')} catch(e) {failure(e)} finally {busy.value = false}
}
async function test(source: PriceSource) {busy.value = true; error.value = ''; result.value = ''; try {const res = await pricing.testSource(source.id); if (!res.ok) error.value = res.error || t('pricing.error'); else result.value = t('pricing.testOk', {count: res.count ?? 0})} catch(e) {failure(e)} finally {busy.value = false}}
async function sync(source: PriceSource) {busy.value = true; error.value = ''; try {const res = await pricing.syncSource(source.id); emit('preview', res.run)} catch(e) {failure(e)} finally {busy.value = false; emit('changed')}}
async function remove() {if (!deleting.value) return; busy.value = true; error.value = ''; try {await pricing.deleteSource(props.revision, deleting.value.id); deleting.value = null; emit('changed')} catch(e) {failure(e)} finally {busy.value = false}}
</script>
<template>
 <section class="pricing-form"><div class="pricing-toolbar"><button class="btn btn-primary" :disabled="disabled || busy" @click="edit()">{{ t('pricing.newSource') }}</button></div><p v-if="error && !open && !deleting" role="alert" class="pricing-error">{{ error }}</p><output v-if="result" aria-live="polite">{{ result }}</output><p v-if="!sources.length">{{ t('pricing.noSources') }}</p>
 <article v-for="source in sources" :key="source.id" class="pricing-card"><header class="pricing-toolbar"><strong>{{ source.name }}</strong><code>{{ source.id }}</code><span>{{ source.enabled ? t('pricing.enabled') : t('pricing.blocked') }}</span><span v-if="source.auto_apply">{{ t('pricing.autoApply') }}</span></header><p class="pricing-url">{{ source.url }}</p><dl class="pricing-grid"><div><dt>{{ t('pricing.lastAttempt') }}</dt><dd>{{ formatDateTime(source.last_attempt_at ?? 0) }}</dd></div><div><dt>{{ t('pricing.lastSuccess') }}</dt><dd>{{ formatDateTime(source.last_success_at ?? 0) }}</dd></div><div><dt>{{ t('pricing.candidates') }}</dt><dd>{{ source.candidate_count ?? '—' }}</dd></div></dl><p v-if="source.last_error" class="pricing-error">{{ source.last_error }}</p><div class="pricing-toolbar"><button class="btn btn-sm" :disabled="busy || disabled" @click="edit(source)">{{ t('pricing.editSource') }}</button><button class="btn btn-sm" :disabled="busy || disabled" @click="test(source)">{{ t('pricing.test') }}</button><button class="btn btn-sm btn-primary" :disabled="busy || disabled" @click="sync(source)">{{ t('pricing.sync') }}</button><button class="btn btn-sm" :disabled="busy || disabled" @click="deleting = source; error = ''">{{ t('pricing.deleteSource') }}</button></div></article>
 <h3>{{ t('pricing.runs') }}</h3><p v-if="!runs.length">{{ t('pricing.noRuns') }}</p><div v-for="run in runs" :key="run.id" class="pricing-card pricing-toolbar"><span>{{ formatDateTime(run.created_at) }} · {{ run.source_id }} · {{ run.status }} · {{ run.items?.length ?? 0 }}</span><button class="btn btn-sm" @click="emit('preview', run)">{{ t('pricing.view') }}</button></div>
 </section>
 <Modal :open="open" :title="t(editing ? 'pricing.editSource' : 'pricing.newSource')" @close="!busy && (open = false)"><form id="pricing-source-form" class="pricing-form" @submit.prevent="save"><label>{{ t('pricing.sourceId') }}<input v-model="form.id" class="input" required :readonly="editing" pattern="[A-Za-z0-9_.-]+" /></label><label>{{ t('pricing.name') }}<input v-model="form.name" class="input" required /></label><label>{{ t('pricing.format') }}<select v-model="form.format" class="input" :aria-label="t('pricing.format')"><option value="standard">Standard JSON</option><option value="models_dev">models.dev</option></select></label><label>{{ t('pricing.url') }}<input v-model="form.url" class="input" type="url" required /></label><div class="pricing-grid"><label>{{ t('pricing.interval') }}<input v-model.number="form.interval_hours" class="input" type="number" min="1" required /></label><label>{{ t('pricing.proxyId') }}<input v-model.number="form.proxy_id" class="input" type="number" min="1" /></label></div><label class="pricing-check"><input v-model="form.enabled" type="checkbox" />{{ t('pricing.enabled') }}</label><label class="pricing-check"><input v-model="form.auto_apply" type="checkbox" />{{ t('pricing.autoApply') }}</label><p class="pricing-hint">{{ t('pricing.autoHint') }}</p><label>{{ t('pricing.trustedHosts') }}<textarea v-model="hosts" class="input" rows="2" /></label><p class="pricing-warning">{{ t('pricing.trustedHint') }}</p><div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button type="button" class="btn btn-sm" :disabled="busy || disabled" @click="emit('changed')">{{ t('pricing.refresh') }}</button></div></form><template #footer><button class="btn" :disabled="busy" @click="open = false">{{ t('pricing.cancel') }}</button><button type="submit" form="pricing-source-form" class="btn btn-primary" :disabled="busy">{{ t('pricing.saveSource') }}</button></template></Modal>
 <Modal :open="!!deleting" :title="t('pricing.deleteSource')" @close="!busy && (deleting = null)"><p>{{ t('pricing.deleteHint') }} {{ deleting?.name }}</p><div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button type="button" class="btn btn-sm" :disabled="busy || disabled" @click="emit('changed')">{{ t('pricing.refresh') }}</button></div><template #footer><button class="btn" :disabled="busy" @click="deleting = null">{{ t('pricing.cancel') }}</button><button class="btn btn-primary" :disabled="busy" @click="remove">{{ t('pricing.confirm') }}</button></template></Modal>
</template>
