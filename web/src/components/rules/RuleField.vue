<script setup lang="ts">
import { computed } from 'vue'
import type { RuleCondition, RuleField, RuleFieldError, RuleSchema, ValueExpr } from '../../api/rules'
import { useI18n } from '../../i18n'
import { defaultForField, localHelp } from '../../utils/ruleSchema'
import { moveItem } from '../../utils/ruleEditor'
import ValueEditor from './ValueEditor.vue'
import ConditionEditor from './ConditionEditor.vue'

const props = defineProps<{ field: RuleField; modelValue: unknown; path: string; schema: RuleSchema; errors?: RuleFieldError[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: unknown] }>()
const { t, locale } = useI18n()
const set = (value: unknown) => emit('update:modelValue', value)
const fieldErrors = computed(() => (props.errors ?? []).filter(e => e.path === props.path || e.path.startsWith(`${props.path}/`) && props.field.type !== 'condition'))
const items = computed<unknown[]>(() => Array.isArray(props.modelValue) ? props.modelValue : [])
function updateItem(index: number, value: unknown) { set(items.value.map((x, i) => i === index ? value : x)) }
function numberInput(event: Event) {
  const input = event.target as HTMLInputElement
  const number = Number(input.value)
  const valid = input.value.trim() !== '' && Number.isSafeInteger(number)
  input.setCustomValidity(valid ? '' : t('rules.numberError'))
  if (valid) set(number)
}
</script>
<template>
  <div class="space-y-1.5" :data-rule-path="path" :data-renderer="field.type">
    <div class="flex flex-wrap items-center gap-2">
      <label :for="`rule-field-${path}`" class="text-[12px] font-medium">{{ field.name }}</label>
      <span class="text-[10px] text-[color:var(--color-ink-faint)]">{{ field.required ? t('rules.required') : t('rules.optional') }} · {{ field.type }}</span>
      <button v-if="!field.required && !field.readonly" class="btn btn-ghost !px-1.5 !py-0.5 text-[10px]" type="button" @click="set(modelValue === undefined ? defaultForField(field) : undefined)">{{ modelValue === undefined ? t('rules.include') : t('rules.unset') }}</button>
      <details class="text-[11px] text-[color:var(--color-ink-muted)]">
        <summary class="cursor-pointer">{{ t('rules.fieldHelp') }}</summary>
        <div class="my-2 space-y-1 whitespace-normal rounded border border-[color:var(--color-line)] p-2">
          <p>{{ localHelp(field.description, locale) }}</p>
          <p>{{ t('rules.default') }}: <code>{{ field.default === undefined ? t('rules.none') : JSON.stringify(field.default) }}</code></p>
          <p v-if="field.enum?.length">{{ t('rules.options') }}: {{ field.enum.join(' / ') }}</p>
          <p v-if="field.min !== undefined">{{ t('rules.minimum') }}: {{ field.min }}</p><p v-if="field.max !== undefined">{{ t('rules.maximum') }}: {{ field.max }}</p>
          <p v-if="field.depends_on"><code>{{ JSON.stringify(field.depends_on) }}</code></p>
          <p v-for="(example, i) in field.examples" :key="i">{{ t('rules.examples') }}: <code>{{ JSON.stringify(example) }}</code></p>
        </div>
      </details>
    </div>
    <div v-if="modelValue === undefined && !field.required" class="text-[11px] text-[color:var(--color-ink-faint)]">{{ t('rules.none') }}</div>
    <template v-else>
      <select v-if="field.enum?.length" :id="`rule-field-${path}`" class="input" :disabled="field.readonly" :value="modelValue" :aria-invalid="!!fieldErrors.length" @change="set(($event.target as HTMLSelectElement).value)"><option v-for="option in field.enum" :key="option" :value="option">{{ option }}</option></select>
      <textarea v-else-if="field.type === 'string'" :id="`rule-field-${path}`" class="input" rows="2" :readonly="field.readonly" :value="String(modelValue ?? '')" :required="field.non_empty" :aria-invalid="!!fieldErrors.length" @input="set(($event.target as HTMLTextAreaElement).value)" />
      <input v-else-if="field.type === 'number'" :id="`rule-field-${path}`" class="input" type="number" step="1" required :readonly="field.readonly" :min="field.min" :max="field.max" :value="modelValue ?? 0" :aria-invalid="!!fieldErrors.length" @input="numberInput" />
      <input v-else-if="field.type === 'boolean'" :id="`rule-field-${path}`" type="checkbox" :disabled="field.readonly" :checked="!!modelValue" @change="set(($event.target as HTMLInputElement).checked)" />
      <ValueEditor v-else-if="field.type === 'value'" :model-value="(modelValue ?? null) as ValueExpr" :schema="schema" @update:model-value="set" />
      <ConditionEditor v-else-if="field.type === 'condition'" :model-value="(modelValue ?? {op:'always'}) as RuleCondition" :schema="schema" :path="path" :errors="errors" @update:model-value="set" />
      <div v-else-if="field.type === 'strings' || field.type === 'values'" class="space-y-2">
        <div v-for="(item, index) in items" :key="index" class="flex items-start gap-1">
          <input v-if="field.type === 'strings'" class="input min-w-0 flex-1" :value="item" :aria-label="`${field.name} ${index}`" @input="updateItem(index, ($event.target as HTMLInputElement).value)" />
          <ValueEditor v-else class="min-w-0 flex-1" :model-value="item as ValueExpr" :schema="schema" @update:model-value="updateItem(index, $event)" />
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === 0" :title="t('rules.up')" @click="set(moveItem(items, index, -1))">↑</button>
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === items.length - 1" :title="t('rules.down')" @click="set(moveItem(items, index, 1))">↓</button>
          <button type="button" class="btn btn-ghost !px-2" :title="t('rules.remove')" @click="set(items.filter((_, i) => i !== index))">×</button>
        </div>
        <button type="button" class="btn btn-ghost" @click="set([...items, field.type === 'strings' ? '' : null])">{{ t('rules.addItem') }}</button>
      </div>
    </template>
    <p v-for="error in fieldErrors" :key="error.path + error.message" class="text-xs text-red-500" role="alert">{{ error.path }}: {{ error.message }}</p>
  </div>
</template>
