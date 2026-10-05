<script setup lang="ts">
import { computed } from 'vue'
import type { JsonValue, RuleSchema, RuleSource, ValueExpr } from '../../api/rules'
import { useI18n } from '../../i18n'
import JsonValueEditor from './JsonValueEditor.vue'

const props = withDefaults(defineProps<{ modelValue: ValueExpr; schema?: RuleSchema; allowReference?: boolean }>(), { allowReference: true })
const emit = defineEmits<{ 'update:modelValue': [value: ValueExpr] }>()
const { t } = useI18n()
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
  <div class="space-y-2">
    <div class="flex flex-wrap items-center gap-2">
      <label class="text-[11px] text-[color:var(--color-ink-muted)]">{{ t('rules.valueKind') }}</label>
      <select class="input !w-auto !py-1 text-[12px]" :value="mode" @change="modeChange(($event.target as HTMLSelectElement).value)">
        <option value="literal">{{ t('rules.literal') }}</option>
        <option v-if="allowReference" value="reference">{{ t('rules.reference') }}</option>
        <option value="escape">{{ t('rules.literal') }} ($literal)</option>
      </select>
    </div>
    <div v-if="mode === 'reference' && reference" class="grid gap-2 sm:grid-cols-3">
      <label class="field-label"><span>source</span><select class="input" :value="reference.source ?? 'current'" @change="updateReference('source', ($event.target as HTMLSelectElement).value as RuleSource)"><option v-for="source in sources" :key="source" :value="source">{{ source }}</option></select></label>
      <label class="field-label sm:col-span-2"><span>path</span><textarea class="input font-mono" rows="2" :value="String(reference.path ?? '')" @input="updateReference('path', ($event.target as HTMLTextAreaElement).value)" /></label>
      <label class="field-label"><span>encoding</span><select class="input" :value="reference.encoding ?? 'value'" @change="updateReference('encoding', ($event.target as HTMLSelectElement).value)"><option value="value">value</option><option value="json">json</option></select></label>
    </div>
    <JsonValueEditor v-else :model-value="literalValue" @update:model-value="mode === 'escape' ? setLiteralEscape($event) : setLiteral($event)" />
  </div>
</template>
