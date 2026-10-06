<script setup lang="ts">
import { computed } from 'vue'
import type { JsonValue, RuleSchema, RuleSource, ValueExpr } from '../../api/rules'
import { useI18n } from '../../i18n'
import JsonValueEditor from './JsonValueEditor.vue'
import {ruleLabel, ruleOptionLabel} from '../../utils/ruleLabels'

const props = withDefaults(defineProps<{ modelValue: ValueExpr; schema?: RuleSchema; allowReference?: boolean; compact?: boolean; idPrefix?: string }>(), { allowReference: true })
const emit = defineEmits<{ 'update:modelValue': [value: ValueExpr] }>()
const { t, locale } = useI18n()
const schema = computed(() => props.schema)
const reference = computed(() => {
  const value = props.modelValue
  return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 1 && '$ref' in value ? (value as { $ref: Record<string, JsonValue> }).$ref : null
})
const literalValue = computed<JsonValue>(() => {
  const value = props.modelValue
  if (value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 1 && '$literal' in value) return (value as { $literal: JsonValue }).$literal
  return value as JsonValue
})
function setLiteral(value: JsonValue) { emit('update:modelValue', value) }
function setReference() { emit('update:modelValue', { $ref: { source: 'current', path: '', encoding: 'value' } }) }
function setLiteralEscape(value: JsonValue) { emit('update:modelValue', { $literal: value }) }
function updateReference(key: string, value: JsonValue) {
  const current = reference.value ?? { source: 'current', path: '', encoding: 'value' }
  emit('update:modelValue', { $ref: { ...current, [key]: value } })
}
function modeChange(mode: string) {
  if (mode === 'reference') setReference()
  else if (mode === 'escape') setLiteralEscape(literalValue.value)
  else setLiteral(literalValue.value)
}
const mode = computed(() => reference.value ? 'reference' : props.modelValue && typeof props.modelValue === 'object' && !Array.isArray(props.modelValue) && Object.keys(props.modelValue).length === 1 && '$literal' in props.modelValue ? 'escape' : 'literal')
const sources = computed(() => schema.value?.sources ?? ['current', 'client', 'context', 'item'])
</script>
<template>
  <div class="space-y-2" :class="{'value-editor-compact': compact}">
    <div class="flex flex-wrap items-center gap-2">
      <label :for="idPrefix" class="text-[11px] text-[color:var(--color-ink-muted)]">{{ t('rules.valueKind') }}</label>
      <select :id="idPrefix" class="input !w-auto !py-1 text-[12px]" :aria-label="t('rules.valueKind')" :value="mode" @change="modeChange(($event.target as HTMLSelectElement).value)">
        <option value="literal">{{ t('rules.literal') }}</option>
        <option v-if="allowReference" value="reference">{{ t('rules.reference') }}</option>
        <option value="escape">{{ t('rules.literal') }} ($literal)</option>
      </select>
    </div>
    <p v-if="compact && mode === 'reference' && reference" class="truncate text-[11px] text-[color:var(--color-ink-muted)]" :title="String(reference.path ?? '')">{{ ruleLabel('source', String(reference.source ?? 'current'), locale) }} · {{ reference.path || '/' }} · {{ t('rules.canvas.inlineAdvanced') }}</p>
    <div v-else-if="mode === 'reference' && reference" class="grid gap-2 sm:grid-cols-3">
      <label class="field-label"><span>{{ ruleLabel('field','source',locale) }}</span><select class="input" :value="reference.source ?? 'current'" @change="updateReference('source', ($event.target as HTMLSelectElement).value as RuleSource)"><option v-for="source in sources" :key="source" :value="source">{{ ruleLabel('source',source,locale) }}</option></select></label>
      <label class="field-label sm:col-span-2"><span>{{ ruleLabel('field','path',locale) }}</span><textarea class="input font-mono" rows="2" :value="String(reference.path ?? '')" @input="updateReference('path', ($event.target as HTMLTextAreaElement).value)" /></label>
      <label class="field-label"><span>{{ ruleLabel('field','encoding',locale) }}</span><select class="input" :value="reference.encoding ?? 'value'" @change="updateReference('encoding', ($event.target as HTMLSelectElement).value)"><option value="value">{{ ruleOptionLabel('value',locale) }}</option><option value="json">{{ ruleOptionLabel('json',locale) }}</option></select></label>
    </div>
    <JsonValueEditor v-else :model-value="literalValue" :compact="compact" :id-prefix="idPrefix ? `${idPrefix}-literal` : undefined" @update:model-value="mode === 'escape' ? setLiteralEscape($event) : setLiteral($event)" />
  </div>
</template>
