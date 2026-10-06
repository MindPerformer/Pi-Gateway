<script setup lang="ts">
import { computed } from 'vue'
import type { RuleCondition, RuleField, RuleFieldError, RuleSchema, ValueExpr } from '../../api/rules'
import { useI18n } from '../../i18n'
import { defaultForField, localHelp } from '../../utils/ruleSchema'
import { moveItem } from '../../utils/ruleEditor'
import { ruleLabel, ruleOptionLabel } from '../../utils/ruleLabels'
import { samplePathOptions } from '../../utils/samplePaths'
import ValueEditor from './ValueEditor.vue'
import ParameterHelp from './ParameterHelp.vue'
import ActionEditor from './ActionEditor.vue'
import ConditionEditor from './ConditionEditor.vue'

const props = defineProps<{ field: RuleField; modelValue: unknown; path: string; schema: RuleSchema; errors?: RuleFieldError[]; sample?: unknown; phase?: string; compact?: boolean; idPrefix?: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: unknown] }>()
const { t, locale } = useI18n()
const set = (value: unknown) => emit('update:modelValue', value)
const fieldErrors = computed(() => (props.errors ?? []).filter(e => e.path === props.path || e.path.startsWith(`${props.path}/`) && props.field.type !== 'condition'))
const controlId = computed(() => `rule-field-${props.idPrefix ? `${props.idPrefix}-` : ''}${props.path}`)
const samplePathId = computed(() => `sample-path-${props.idPrefix ? `${props.idPrefix}-` : ''}${props.path}`)
// Missing optional fields stay missing until a control is actually changed.
const fieldValue = computed(() => props.compact && props.modelValue === undefined ? defaultForField(props.field) : props.modelValue)
const items = computed<unknown[]>(() => Array.isArray(fieldValue.value) ? fieldValue.value : [])
const visibleItems = computed(() => props.compact ? items.value.slice(0, 3) : items.value)
const samplePaths = computed(() => props.sample !== undefined && props.field.type === 'string' && ['path','source_path','target_path'].includes(props.field.name) ? samplePathOptions(props.sample) : [])
function selectSamplePath(event: Event) {
  const select = event.target as HTMLSelectElement
  if (samplePaths.value.some(option=>option.path===select.value)) set(select.value)
  select.value = '__sample_path_placeholder__'
}
function updateItem(index: number, value: unknown) { set(items.value.map((x, i) => i === index ? value : x)) }
function textInput(event: Event, index?: number) {
  if (props.compact && (event as InputEvent).isComposing) return
  const value = (event.target as HTMLInputElement | HTMLTextAreaElement).value
  if (index === undefined) set(value)
  else updateItem(index, value)
}
function numberInput(event: Event) {
  const input = event.target as HTMLInputElement
  const number = Number(input.value)
  const valid = input.value.trim() !== '' && Number.isSafeInteger(number)
  input.setCustomValidity(valid ? '' : t('rules.numberError'))
  if (valid) set(number)
}
</script>
<template>
  <div class="space-y-1.5" :class="{'rule-field-compact': compact}" :data-rule-path="path" :data-renderer="field.type">
    <div class="flex flex-wrap items-center gap-2">
      <label :id="`${controlId}-label`" :for="controlId" class="text-[12px] font-medium" :title="compact ? localHelp(field.description, locale) : undefined">{{ field.label?.split(' / ')[locale==='zh-CN'?0:1] ?? ruleLabel('field', field.name, locale) }}</label><ParameterHelp :field="field" />
      <span v-if="!compact" class="text-[10px] text-[color:var(--color-ink-faint)]">{{ field.required ? t('rules.required') : t('rules.optional') }} · {{ ruleOptionLabel(field.type, locale) }}</span>
      <span v-else-if="field.required" class="text-[10px] text-[color:var(--color-ink-faint)]">{{ t('rules.required') }}</span>
      <button v-if="!field.required && !field.readonly" class="btn btn-ghost !px-1.5 !py-0.5 text-[10px]" type="button" @click="set(modelValue === undefined ? defaultForField(field) : undefined)">{{ modelValue === undefined ? t('rules.include') : t('rules.unset') }}</button>
      <details v-if="!compact" class="text-[11px] text-[color:var(--color-ink-muted)]">
        <summary class="cursor-pointer">{{ t('rules.fieldHelp') }}</summary>
        <div class="my-2 space-y-1 whitespace-normal rounded border border-[color:var(--color-line)] p-2">
          <p>{{ localHelp(field.description, locale) }}</p>
          <p>{{ t('rules.default') }}: <code>{{ field.default === undefined ? t('rules.none') : JSON.stringify(field.default) }}</code></p>
          <p v-if="field.enum?.length">{{ t('rules.options') }}: {{ field.enum.map(option => ruleOptionLabel(option, locale)).join(' / ') }}</p>
          <p v-if="field.min !== undefined">{{ t('rules.minimum') }}: {{ field.min }}</p><p v-if="field.max !== undefined">{{ t('rules.maximum') }}: {{ field.max }}</p>
          <p v-if="field.depends_on"><code>{{ JSON.stringify(field.depends_on) }}</code></p>
          <p v-for="(example, i) in field.examples" :key="i">{{ t('rules.examples') }}: <code>{{ JSON.stringify(example) }}</code></p>
        </div>
      </details>
    </div>
    <div v-if="samplePaths.length" data-testid="sample-field-picker" class="space-y-1">
      <label v-if="!compact" :for="samplePathId" class="text-[11px]">{{ t('rules.canvas.samplePaths') }} · {{ ruleLabel('field',field.name,locale) }}</label>
      <select :id="samplePathId" :aria-label="`${t('rules.canvas.samplePaths')} · ${ruleLabel('field', field.name, locale)}`" :data-testid="`sample-path-select-${field.name}`" class="input" value="__sample_path_placeholder__" @change="selectSamplePath">
        <option value="__sample_path_placeholder__" disabled>{{ t('rules.canvas.samplePaths') }}</option>
        <option v-for="option in samplePaths" :key="option.path" :value="option.path">{{ option.path === '' ? t('rules.canvas.samplePathRoot') : option.label }}</option>
      </select>
      <p v-if="!compact" class="text-[10px] text-[color:var(--color-ink-muted)]">{{ t('rules.canvas.samplePathHint') }}</p>
    </div>
    <div v-if="modelValue === undefined && !field.required" class="text-[11px] text-[color:var(--color-ink-faint)]">{{ t('rules.none') }}</div>
    <template v-if="compact || modelValue !== undefined || field.required">
      <select v-if="field.enum?.length" :id="controlId" class="input" :disabled="field.readonly" :value="fieldValue" :aria-invalid="!!fieldErrors.length" @change="set(($event.target as HTMLSelectElement).value)"><option v-for="option in field.enum" :key="option" :value="option">{{ ruleOptionLabel(option, locale) }}</option></select>
      <textarea v-else-if="field.type === 'string'" :id="controlId" class="input" :rows="compact ? 1 : 2" :readonly="field.readonly" :value="String(fieldValue ?? '')" :required="field.non_empty" :aria-invalid="!!fieldErrors.length" @input="textInput" @compositionend="compact && textInput($event)" />
      <input v-else-if="field.type === 'number'" :id="controlId" class="input" type="number" step="1" required :readonly="field.readonly" :min="field.min" :max="field.max" :value="fieldValue ?? 0" :aria-invalid="!!fieldErrors.length" @input="numberInput" />
      <input v-else-if="field.type === 'boolean'" :id="controlId" type="checkbox" :disabled="field.readonly" :checked="!!fieldValue" @change="set(($event.target as HTMLInputElement).checked)" />
      <ValueEditor v-else-if="field.type === 'value'" :model-value="(fieldValue ?? null) as ValueExpr" :schema="schema" :compact="compact" :id-prefix="idPrefix ? controlId : undefined" @update:model-value="set" />
      <ConditionEditor v-else-if="field.type === 'condition'" :model-value="(modelValue ?? {op:'always'}) as RuleCondition" :schema="schema" :path="path" :errors="errors" :sample="sample" @update:model-value="set" />
      <div v-else-if="field.type === 'strings' || field.type === 'values'" :id="controlId" class="space-y-2" role="group" :aria-labelledby="`${controlId}-label`">
        <p v-if="compact && items.length > visibleItems.length" class="text-[10px] text-[color:var(--color-ink-muted)]">{{ visibleItems.length }} / {{ items.length }} · {{ t('rules.canvas.inlineAdvanced') }}</p>
        <div v-for="(item, index) in visibleItems" :key="index" class="flex items-start gap-1">
          <input v-if="field.type === 'strings'" :id="idPrefix ? `${controlId}-${index}` : undefined" class="input min-w-0 flex-1" :value="item" :aria-label="`${ruleLabel('field', field.name, locale)} ${index + 1}`" @input="textInput($event, index)" @compositionend="compact && textInput($event, index)" />
          <ValueEditor v-else class="min-w-0 flex-1" :model-value="item as ValueExpr" :schema="schema" :compact="compact" :id-prefix="idPrefix ? `${controlId}-${index}` : undefined" @update:model-value="updateItem(index, $event)" />
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === 0" :title="t('rules.up')" @click="set(moveItem(items, index, -1))">↑</button>
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === items.length - 1" :title="t('rules.down')" @click="set(moveItem(items, index, 1))">↓</button>
          <button type="button" class="btn btn-ghost !px-2" :title="t('rules.remove')" @click="set(items.filter((_, i) => i !== index))">×</button>
        </div>
        <button type="button" class="btn btn-ghost" @click="set([...items, field.type === 'strings' ? '' : null])">{{ t('rules.addItem') }}</button>
      </div>
    </template>
    <ActionEditor v-if="field.type === 'action_array'" :model-value="(modelValue ?? []) as import('../../api/rules').RuleAction[]" :schema="schema" :phase="phase ?? 'request'" :base-path="path" :errors="errors" :sample="sample" @update:model-value="set" />
    <p v-for="error in fieldErrors" :key="error.path + error.message" class="text-xs text-red-500" role="alert">{{ error.path }}: {{ error.message }}</p>
  </div>
</template>
<style scoped>
.rule-field-compact { min-width: 0; }
.rule-field-compact :deep(.input) { min-width: 0; padding: .3rem .45rem; font-size: .72rem; }
.rule-field-compact :deep(textarea) { resize: vertical; min-height: 1.8rem; max-height: 8rem; }
.rule-field-compact :deep(.btn) { font-size: .66rem; }
.rule-field-compact [role="alert"] { overflow-wrap: anywhere; font-size: .65rem; }
</style>
