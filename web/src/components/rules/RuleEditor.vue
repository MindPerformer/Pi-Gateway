<script setup lang="ts">
import { computed, ref, shallowRef } from 'vue'
import { api, ApiError } from '../../api/client'
import type { JsonValue, Rule, RuleFieldError, RuleSchema, RuleSimulationResult } from '../../api/rules'
import { useI18n } from '../../i18n'
import { useToastStore } from '../../stores/ui'
import { clone, parseRule, RuleInputError, stringifyRule, validateRule, type RuleDraft } from '../../utils/ruleEditor'
import { readonlyRuleFields } from '../../utils/ruleSchemaAdapter'
import RuleField from './RuleField.vue'
import ConditionEditor from './ConditionEditor.vue'
import ActionEditor from './ActionEditor.vue'
import JsonValueEditor from './JsonValueEditor.vue'
import RuleHelp from './RuleHelp.vue'
import JsonViewer from '../JsonViewer.vue'
const props = defineProps<{ modelValue: RuleDraft; schema: RuleSchema; busy?: boolean; conflict?: Rule | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: RuleDraft]; save: [rule: Rule]; close: []; reload: []; rebase: [] }>()
const { t } = useI18n()
const toast = useToastStore()
const form = ref<HTMLFormElement>()
const rule = computed(() => props.modelValue.rule)
const normalFields = computed(() => props.schema.rule_fields.filter(f => !['when', 'actions'].includes(f.name) && !readonlyRuleFields.includes(f.name)))
const sample = shallowRef<JsonValue>({ model: 'old-model', input: [] })
const clientBody = shallowRef<JsonValue>({})
const context = shallowRef<JsonValue>({ model: 'old-model', original_model: 'old-model', request_path: '/v1/responses', request_method: 'POST', api_key_id: 1, client_protocol: 'http' })
const preview = ref<unknown>()
const simulating = ref(false)
const validating = ref(false)
function patch(fields: Partial<RuleDraft>) { emit('update:modelValue', { ...props.modelValue, ...fields }) }
function updateRule(next: Rule) { patch({rule: next, code: stringifyRule(next), errors: []}) }
function updateField(name: string, value: unknown) { const next = {...rule.value} as unknown as Record<string, unknown>; if (value === undefined) delete next[name]; else next[name] = value; updateRule(next as unknown as Rule) }
function reportErrors(error: unknown) {
  let errors: RuleFieldError[] = []
  if (error instanceof RuleInputError) errors = error.errors
  else if (error instanceof ApiError && error.payload && typeof error.payload === 'object') {
    const payload = error.payload as {errors?: RuleFieldError[]; error?: {path?: string; message?: string}}
    if (Array.isArray(payload.errors)) errors = payload.errors
  }
  if (!errors.length) errors = [{path:'', message: error instanceof Error ? error.message : String(error)}]
  patch({ errors })
}
function synchronize(): Rule | undefined {
  try {
    if (!form.value?.reportValidity()) return
    const next = props.modelValue.mode === 'code' ? parseRule(props.modelValue.code, props.schema) : clone(rule.value)
    const errors = validateRule(next, props.schema)
    if (errors.length) { patch({errors}); return }
    patch({rule: next, errors: []})
    return next
  } catch (error) { reportErrors(error) }
}
function switchMode(mode: 'visual' | 'code') {
  if (mode === props.modelValue.mode) return
  if (mode === 'visual') { const next = synchronize(); if (next) patch({rule: next, code: stringifyRule(next), mode, errors: []}) }
  else if (form.value?.reportValidity()) patch({code: stringifyRule(rule.value), mode})
}
function formatCode() { try { const next = parseRule(props.modelValue.code, props.schema); patch({rule: next, code: stringifyRule(next), errors: []}) } catch (error) { reportErrors(error) } }
async function validate() {
  const next = synchronize(); if (!next) return
  validating.value = true
  try { const result = await api.validateRule(next); patch({ errors: result.errors ?? [] }); if (result.valid) toast.success(t('rules.validate')) } catch (error) { reportErrors(error) } finally { validating.value = false }
}
function save() { const next = synchronize(); if (next) emit('save', next) }
async function simulate() {
  const next = synchronize(); if (!next) return
  if (!context.value || Array.isArray(context.value) || typeof context.value !== 'object') { patch({errors:[{path:'/simulation/context',message:t('rules.simulationNote')}]}); return }
  simulating.value = true
  try {
    const result = await api.simulateRules({rule: next, phase: next.phase, input: {body: sample.value, client_body: clientBody.value, context: context.value, model: typeof context.value.model === 'string' ? context.value.model : '', event_type: typeof context.value.event_type === 'string' ? context.value.event_type : ''}})
    preview.value = result
    if (result.errors?.length) patch({errors: result.errors})
  } catch (error) { reportErrors(error) } finally { simulating.value = false }
}
function loadExample(example: Rule) {
  if (!confirm(t('rules.exampleConfirm'))) return
  const next = clone(example)
  // Examples change only editable content; they never replace the selected persisted identity.
  for (const key of readonlyRuleFields) { const current = (rule.value as unknown as Record<string, unknown>)[key]; if (current === undefined) delete (next as unknown as Record<string, unknown>)[key]; else (next as unknown as Record<string, unknown>)[key] = current }
  updateRule(next)
}
async function copyCode() { try { await navigator.clipboard.writeText(props.modelValue.mode === 'code' ? props.modelValue.code : stringifyRule(rule.value)) } catch { toast.error(t('common.clipboardBlocked')) } }
</script>
<template>
  <form ref="form" class="space-y-4" @submit.prevent="save">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-base font-semibold">{{ rule.name || t('rules.add') }}</h2>
      <div class="flex flex-wrap gap-2"><button type="button" class="btn" @click="emit('close')">{{ t('rules.cancel') }}</button><button class="btn btn-primary" :disabled="busy" type="submit">{{ t('rules.save') }}</button></div>
    </div>
    <div v-if="conflict" class="notice space-y-2" role="alert"><p>{{ t('rules.conflict') }}</p><details><summary>{{ t('rules.serverVersion') }}</summary><pre class="max-h-72 overflow-auto whitespace-pre-wrap text-xs">{{ JSON.stringify(conflict, null, 2) }}</pre></details><div class="flex flex-wrap gap-2"><button type="button" class="btn" @click="emit('reload')">{{ t('rules.useServer') }}</button><button type="button" class="btn" @click="emit('rebase')">{{ t('rules.rebase') }}</button></div></div>
    <div class="flex flex-wrap gap-2"><button type="button" class="btn" :aria-pressed="modelValue.mode === 'visual'" @click="switchMode('visual')">{{ t('rules.graphical') }}</button><button type="button" class="btn" :aria-pressed="modelValue.mode === 'code'" @click="switchMode('code')">{{ t('rules.code') }}</button><button type="button" class="btn" :disabled="validating || busy" @click="validate">{{ t('rules.validate') }}</button></div>
    <div v-if="modelValue.errors.length" class="rounded border border-red-500 p-3 text-xs text-red-500" role="alert"><h3 class="font-medium">{{ t('rules.errors') }}</h3><p v-for="(error, index) in modelValue.errors" :key="index" class="mt-1 break-words"><code>{{ error.path || '/' }}</code>: {{ error.message }}</p></div>
    <template v-if="modelValue.mode === 'visual'">
      <div class="grid gap-3 sm:grid-cols-2"><RuleField v-for="field in normalFields" :key="field.name" :field="field" :model-value="(rule as unknown as Record<string, unknown>)[field.name]" :schema="schema" :path="`/${field.name}`" :errors="modelValue.errors" @update:model-value="updateField(field.name, $event)" /></div>
      <details v-if="rule.id" class="text-xs text-[color:var(--color-ink-muted)]"><summary>{{ t('rules.readonly') }}</summary><dl><div v-for="key in readonlyRuleFields" :key="key" class="flex gap-2"><dt>{{ key }}</dt><dd>{{ (rule as unknown as Record<string, unknown>)[key] }}</dd></div></dl></details>
      <section class="space-y-2"><h3 class="text-sm font-semibold">{{ t('rules.when') }}</h3><ConditionEditor :model-value="rule.when" :schema="schema" :errors="modelValue.errors" @update:model-value="updateField('when', $event)" /></section>
      <section class="space-y-2"><h3 class="text-sm font-semibold">{{ t('rules.then') }}</h3><ActionEditor :model-value="rule.actions" :schema="schema" :phase="rule.phase" :errors="modelValue.errors" @update:model-value="updateField('actions', $event)" /></section>
    </template>
    <div v-else class="space-y-2"><div class="flex gap-2"><button type="button" class="btn" @click="formatCode">{{ t('rules.format') }}</button><button type="button" class="btn" @click="copyCode">{{ t('rules.copyCode') }}</button></div><textarea class="input min-h-[28rem] font-mono text-xs" spellcheck="false" :value="modelValue.code" :aria-label="t('rules.code')" @input="patch({code:($event.target as HTMLTextAreaElement).value})" /></div>
    <RuleHelp :schema="schema" @example="loadExample" />
    <details class="rounded-lg border border-[color:var(--color-line)] p-3"><summary class="cursor-pointer text-sm font-medium">{{ t('rules.simulate') }}</summary><div class="mt-3 space-y-3"><p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.simulationNote') }}</p><h4 class="text-xs font-medium">{{ t('rules.input') }}</h4><JsonValueEditor v-model="sample" /><h4 class="text-xs font-medium">{{ t('rules.clientBody') }}</h4><JsonValueEditor v-model="clientBody" /><h4 class="text-xs font-medium">{{ t('rules.context') }}</h4><JsonValueEditor v-model="context" /><button type="button" class="btn" :disabled="simulating" @click="simulate">{{ t('rules.simulate') }}</button><section v-if="preview" class="space-y-2"><h4 class="text-xs font-medium">{{ t('rules.preview') }}</h4><JsonViewer :value="preview" /></section></div></details>
  </form>
</template>
