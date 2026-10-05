<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { api, ApiError } from '../api/client'
import type { Rule, RuleFieldError, RuleSchema } from '../api/rules'
import { useToastStore } from '../stores/ui'
import { useI18n } from '../i18n'
import { newRule, ruleSchema } from '../utils/ruleSchema'
import { adaptSchema } from '../utils/ruleSchemaAdapter'
import { clone, conditionSummary, createDraft, draftDirty, stringifyRule, type RuleDraft } from '../utils/ruleEditor'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/Badge.vue'
import Toggle from '../components/Toggle.vue'
import RuleEditor from '../components/rules/RuleEditor.vue'
import LegacyRepair from '../components/rules/LegacyRepair.vue'
import { RefreshCw, Plus } from 'lucide-vue-next'
const toast = useToastStore()
const { t } = useI18n()
const rules = ref([]) as Ref<Rule[]>
const schema = shallowRef<RuleSchema>(ruleSchema)
const schemaWarning = ref('')
const schemaUnsupported = ref(false)
const total = ref(0)
const version = ref(0)
const loading = ref(false)
const loadError = ref('')
const legacyOpen = ref(false)
const legacyDirty = ref(false)
const busy = ref(false)
const filters = ref({ search: '', phase: '', enabled: '', limit: 20, offset: 0 })
const selected = ref<string[]>([])
const active = ref('')
const drafts = ref({}) as Ref<Record<string, RuleDraft>>
const conflicts = ref({}) as Ref<Record<string, Rule>>
const current = computed(() => drafts.value[active.value])
const page = computed(() => Math.floor(filters.value.offset / filters.value.limit) + 1)
const pages = computed(() => Math.max(1, Math.ceil(total.value / filters.value.limit)))
const unsaved = computed(() => Object.entries(drafts.value).filter(([, draft]) => draftDirty(draft)))
let loadSequence = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
function message(error: unknown) { return error instanceof Error ? error.message : String(error) }
async function load() {
  const sequence = ++loadSequence
  loading.value = true
  try {
    const result = await api.listRules({search: filters.value.search, phase: filters.value.phase, enabled: filters.value.enabled === '' ? undefined : filters.value.enabled === 'true', limit: filters.value.limit, offset: filters.value.offset})
    if (sequence !== loadSequence) return
    loadError.value = ''
    rules.value = result.rules ?? []
    total.value = result.total ?? 0
    if (filters.value.offset > 0 && filters.value.offset >= total.value) { filters.value.offset = Math.max(0, Math.ceil(total.value / filters.value.limit) - 1) * filters.value.limit; void load(); return }
    version.value = result.version ?? 0
    selected.value = selected.value.filter(id => rules.value.some(r => r.id === id))
    for (const row of rules.value) {
      const draft = row.id ? drafts.value[row.id] : undefined
      if (!draft) continue
      if (!draftDirty(draft)) drafts.value[row.id!] = createDraft(row)
      else if (row.revision !== draft.revision) conflicts.value[row.id!] = row
    }
  } catch (error) { if (sequence === loadSequence) { loadError.value = message(error); toast.error(message(error)) } } finally { if (sequence === loadSequence) loading.value = false }
}
async function loadSchema() {
  try { const raw = await api.getRuleSchema(); try { schema.value = adaptSchema(raw); schemaWarning.value = ''; schemaUnsupported.value = false } catch (error) { schemaWarning.value = `${t('rules.schemaUnsupported')} ${message(error)}`; schemaUnsupported.value = true } }
  catch { schemaWarning.value = t('rules.schemaUnavailable') }
}
onMounted(() => { void loadSchema(); void load(); window.addEventListener('beforeunload', leaveWindow) })
onBeforeUnmount(() => { loadSequence++; clearTimeout(searchTimer); window.removeEventListener('beforeunload', leaveWindow) })
watch(() => filters.value.search, () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { filters.value.offset = 0; void load() }, 250) })
watch(() => [filters.value.phase, filters.value.enabled, filters.value.limit], () => { filters.value.offset = 0; void load() })
onBeforeRouteLeave(() => !(unsaved.value.length || legacyDirty.value) || confirm(t('rules.leaveConfirm')))
function leaveWindow(event: BeforeUnloadEvent) { if (unsaved.value.length || legacyDirty.value) { event.preventDefault(); event.returnValue = '' } }
function edit(rule: Rule) { if (!rule.id) return; if (!drafts.value[rule.id]) drafts.value[rule.id] = createDraft(rule); active.value = rule.id }
function add() { const key = `new:${crypto.randomUUID()}`; drafts.value[key] = createDraft(newRule(), true); active.value = key }
function changePage(next: number) { filters.value.offset = Math.max(0, Math.min(pages.value - 1, next - 1)) * filters.value.limit; void load() }
async function failure(error: unknown, id?: string) {
  toast.error(message(error))
  if (id && error instanceof ApiError && error.status === 409) { const result = await api.getRule(id).catch(() => null); if (result) conflicts.value[id] = result.rule; return }
  const payload = error instanceof ApiError && error.payload && typeof error.payload === 'object' ? error.payload as { errors?: RuleFieldError[] } : undefined
  if (id && drafts.value[id]) drafts.value[id]!.errors = payload?.errors?.length ? payload.errors : [{path:'', message: message(error)}]
}
async function save(rule: Rule) {
  const key = active.value
  const draft = drafts.value[key]
  if (!draft || busy.value) return
  busy.value = true
  try {
    const valid = await api.validateRule(rule)
    if (!valid.valid) { draft.errors = valid.errors ?? []; return }
    const result = draft.isNew ? await api.createRule(rule, version.value) : await api.updateRule(key, rule, draft.revision)
    delete drafts.value[key]; delete conflicts.value[key]
    if (result.rule.id) { drafts.value[result.rule.id] = createDraft(result.rule); active.value = result.rule.id }
    toast.success(t('rules.saved')); await load()
  } catch (error) { await failure(error, key) } finally { busy.value = false }
}
async function toggle(rule: Rule, enabled: boolean) {
  if (!rule.id || busy.value) return
  busy.value = true
  try { await api.batchRules([{id:rule.id, expected_revision:rule.revision}], enabled, version.value); await load() } catch (error) { await failure(error, rule.id) } finally { busy.value = false }
}
async function batch(enabled: boolean) {
  if (!selected.value.length || busy.value) return
  busy.value = true
  try { await api.batchRules(rules.value.filter(r => r.id && selected.value.includes(r.id)).map(r => ({id:r.id!, expected_revision:r.revision})), enabled, version.value); selected.value = []; await load() } catch (error) { await failure(error) } finally { busy.value = false }
}
async function duplicate(rule: Rule) {
  if (!rule.id || busy.value) return
  busy.value = true
  try { const result = await api.duplicateRule(rule.id, rule.revision); await load(); edit(result.rule) } catch (error) { await failure(error, rule.id) } finally { busy.value = false }
}
async function remove(rule: Rule) {
  if (!rule.id || busy.value || !confirm(t('rules.deleteConfirm'))) return
  busy.value = true
  try { await api.deleteRule(rule.id, rule.revision); delete drafts.value[rule.id]; delete conflicts.value[rule.id]; if (active.value === rule.id) active.value = ''; await load() } catch (error) { await failure(error, rule.id) } finally { busy.value = false }
}
async function priority(rule: Rule, event: Event) {
  const input = event.target as HTMLInputElement
  const value = Number(input.value)
  if (!rule.id || !input.reportValidity() || !Number.isSafeInteger(value) || value === rule.priority || busy.value) return
  busy.value = true
  try { await api.updateRule(rule.id, {...rule, priority:value}, rule.revision); await load() } catch (error) { input.value = String(rule.priority); await failure(error, rule.id) } finally { busy.value = false }
}
async function move(rule: Rule, direction: -1 | 1) {
  if (!rule.id || busy.value) return
  busy.value = true
  try {
    // The server resolves the real same-phase neighbour, including across pages/filters.
    await api.moveRule(rule.id, rule.revision!, direction === -1 ? 'up' : 'down', version.value)
    await load()
  } catch (error) { await failure(error, rule.id) } finally { busy.value = false }
}
function reloadDraft() { const server = conflicts.value[active.value]; if (!server || !confirm(t('rules.exampleConfirm'))) return; drafts.value[active.value] = createDraft(server); delete conflicts.value[active.value] }
function rebaseDraft() { const server = conflicts.value[active.value]; const draft = current.value; if (!server || !draft || !confirm(t('rules.rebaseConfirm'))) return; draft.revision = server.revision; draft.rule.revision = server.revision; draft.base = stringifyRule(server); if (draft.mode === 'visual') draft.code = stringifyRule(draft.rule); delete conflicts.value[active.value] }
</script>
<template>
  <div class="page-view">
    <PageHeader :title="t('rules.title')" :subtitle="t('rules.subtitle')"><button class="btn" :disabled="loading || busy" @click="loadSchema(); load()"><RefreshCw class="h-3.5 w-3.5" />{{ t('common.refresh') }}</button><button class="btn btn-primary" :disabled="schemaUnsupported" @click="add"><Plus class="h-3.5 w-3.5" />{{ t('rules.add') }}</button></PageHeader>
    <div class="page-content space-y-4">
      <p v-if="schemaWarning" class="notice" role="alert">{{ schemaWarning }}</p>
      <div v-if="loadError" class="notice space-y-2" role="alert"><p>{{ loadError }}</p><button class="btn" @click="legacyOpen = true">{{ t('rules.legacyRepair') }}</button></div>
      <LegacyRepair v-if="legacyOpen" @dirty="legacyDirty = $event" @retry="load" />
      <div class="flex flex-wrap gap-2"><input v-model="filters.search" class="input min-w-40 flex-1" :placeholder="t('rules.search')" :aria-label="t('rules.search')" /><select v-model="filters.phase" class="input !w-auto"><option value="">{{ t('rules.allPhases') }}</option><option v-for="phase in schema.phases" :key="phase" :value="phase">{{ phase }}</option></select><select v-model="filters.enabled" class="input !w-auto"><option value="">{{ t('rules.allStates') }}</option><option value="true">{{ t('rules.enabled') }}</option><option value="false">{{ t('rules.disabled') }}</option></select><select v-model.number="filters.limit" class="input !w-auto"><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option></select></div>
      <div v-if="selected.length" class="flex gap-2"><button class="btn" :disabled="busy" @click="batch(true)">{{ t('rules.enableSelected') }}</button><button class="btn" :disabled="busy" @click="batch(false)">{{ t('rules.disableSelected') }}</button></div>
      <div v-if="unsaved.length" class="flex flex-wrap gap-2"><button v-for="[key, draft] in unsaved" :key="key" class="btn" @click="active = key">{{ draft.rule.name || t('rules.add') }} · {{ t('rules.dirty') }}</button></div>
      <div class="grid items-start gap-4" :class="current ? 'xl:grid-cols-[minmax(20rem,0.75fr)_minmax(30rem,1.25fr)]' : ''">
        <div class="space-y-3" :aria-busy="loading">
          <div v-if="!loading && !rules.length" class="card empty-state">{{ t('rules.empty') }}</div>
          <article v-for="rule in rules" :key="rule.id" class="card panel-content space-y-3" :class="active === rule.id ? 'ring-1 ring-[color:var(--color-accent)]' : ''">
            <div class="flex items-start gap-2"><input v-if="rule.id" v-model="selected" type="checkbox" :value="rule.id" :aria-label="`${t('rules.select')}: ${rule.name}`" /><Toggle :model-value="rule.enabled" :disabled="busy" @update:model-value="toggle(rule, $event)" /><div class="min-w-0 flex-1"><h2 class="break-words text-sm font-semibold">{{ rule.name }}</h2><p class="text-xs text-[color:var(--color-ink-muted)]">{{ rule.description }}</p></div><Badge :tone="rule.enabled ? 'success' : 'neutral'">{{ rule.enabled ? t('rules.enabled') : t('rules.disabled') }}</Badge></div>
            <div class="flex flex-wrap items-center gap-2 text-[11px]"><Badge tone="neutral">{{ rule.phase }}</Badge><label>{{ t('rules.priority') }} <input class="input !w-28 !py-1" type="number" step="1" min="-2147483648" max="2147483647" required :value="rule.priority" :disabled="busy" @change="priority(rule, $event)" /></label><span v-if="rule.legacy_name">{{ rule.legacy_name }}</span></div>
            <p class="truncate font-mono text-[11px] text-[color:var(--color-ink-muted)]" :title="conditionSummary(rule.when)">{{ conditionSummary(rule.when) }}</p><p class="break-words text-xs">{{ rule.actions.map(a => a.type).join(' → ') || t('rules.noActions') }}</p>
            <div class="flex flex-wrap gap-1"><button class="btn" :disabled="schemaUnsupported" @click="edit(rule)">{{ t('rules.edit') }}</button><button class="btn btn-ghost" :disabled="busy" @click="duplicate(rule)">{{ t('rules.copy') }}</button><button class="btn btn-ghost" :disabled="busy" @click="move(rule, -1)">{{ t('rules.up') }}</button><button class="btn btn-ghost" :disabled="busy" @click="move(rule, 1)">{{ t('rules.down') }}</button><button class="btn btn-ghost" :disabled="busy" @click="remove(rule)">{{ t('rules.delete') }}</button></div>
          </article>
          <div class="flex flex-wrap items-center justify-between gap-2 text-xs"><span>{{ t('rules.pagination', {total, page, pages}) }}</span><div class="flex gap-1"><button class="btn" :disabled="page <= 1 || loading" @click="changePage(page - 1)">{{ t('rules.previous') }}</button><button class="btn" :disabled="page >= pages || loading" @click="changePage(page + 1)">{{ t('rules.next') }}</button></div></div>
        </div>
        <aside v-if="current && !schemaUnsupported" class="card panel-content min-w-0"><RuleEditor :key="active" :model-value="current" :schema="schema" :busy="busy" :conflict="conflicts[active]" @update:model-value="drafts[active] = $event" @save="save" @close="active = ''" @reload="reloadDraft" @rebase="rebaseDraft" /></aside>
      </div>
    </div>
  </div>
</template>
