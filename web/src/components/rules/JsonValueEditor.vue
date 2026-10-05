<script setup lang="ts">
import { computed } from 'vue'
import type { JsonValue } from '../../api/rules'
import { useI18n } from '../../i18n'
import { setObjectProperty } from '../../utils/ruleEditor'

const props = withDefaults(defineProps<{ modelValue: JsonValue; path?: string; depth?: number }>(), { path: '', depth: 0 })
const emit = defineEmits<{ 'update:modelValue': [value: JsonValue] }>()
const { t } = useI18n()
const kind = computed(() => props.modelValue === null ? 'null' : Array.isArray(props.modelValue) ? 'array' : typeof props.modelValue)
const objectEntries = computed(() => props.modelValue && typeof props.modelValue === 'object' && !Array.isArray(props.modelValue) ? Object.entries(props.modelValue) : [])
function update(value: JsonValue) { emit('update:modelValue', value) }
function updateNumber(event: Event) {
  const input = event.target as HTMLInputElement
  const number = Number(input.value)
  const valid = input.value.trim() !== '' && Number.isFinite(number) && (!Number.isInteger(number) || Number.isSafeInteger(number))
  input.setCustomValidity(valid ? '' : t('rules.numberError'))
  if (valid) update(number)
}
function changeKind(next: string) {
  if (next === 'null') update(null)
  else if (next === 'boolean') update(false)
  else if (next === 'number') update(0)
  else if (next === 'string') update('')
  else if (next === 'array') update([])
  else if (next === 'object') update({})
}
function updateArray(index: number, value: JsonValue) { if (Array.isArray(props.modelValue)) update(props.modelValue.map((item, i) => i === index ? value : item)) }
function removeArray(index: number) { if (Array.isArray(props.modelValue)) update(props.modelValue.filter((_, i) => i !== index)) }
function addArray() { if (Array.isArray(props.modelValue)) update([...props.modelValue, null]) }
function updateObject(key: string, value: JsonValue) { if (props.modelValue && typeof props.modelValue === 'object' && !Array.isArray(props.modelValue)) update({...props.modelValue, [key]: value}) }
function removeObject(key: string) { if (props.modelValue && typeof props.modelValue === 'object' && !Array.isArray(props.modelValue)) { const next = {...props.modelValue}; delete next[key]; update(next) } }
function addObject() {
  if (!props.modelValue || typeof props.modelValue !== 'object' || Array.isArray(props.modelValue)) return
  let key = 'property'
  let index = 1
  while (Object.hasOwn(props.modelValue, key)) key = `property_${index++}`
  update(setObjectProperty(props.modelValue, null, key, null))
}
function changeKey(oldKey: string, event: Event) {
  const key = (event.target as HTMLInputElement).value
  if (!props.modelValue || typeof props.modelValue !== 'object' || Array.isArray(props.modelValue)) return
  const input = event.target as HTMLInputElement
  try { const next = setObjectProperty(props.modelValue, oldKey, key, props.modelValue[oldKey]!); input.setCustomValidity(''); update(next) } catch { input.setCustomValidity(t('rules.duplicateKey')); input.reportValidity() }
}
</script>
<template>
  <div class="space-y-2 rounded border border-[color:var(--color-line)] p-2" :class="depth > 7 ? 'ml-0' : ''">
    <div class="flex flex-wrap items-center gap-2">
      <select class="input !w-auto !py-1 text-[12px]" :value="kind" :aria-label="t('rules.valueType')" @change="changeKind(($event.target as HTMLSelectElement).value)">
        <option value="null">null</option><option value="boolean">boolean</option><option value="number">number</option><option value="string">string</option><option value="array">array</option><option value="object">object</option>
      </select>
      <span v-if="kind === 'array'" class="text-[11px] text-[color:var(--color-ink-muted)]">{{ (modelValue as JsonValue[]).length }} {{ t('rules.addItem') }}</span>
      <button v-if="kind === 'array'" type="button" class="btn btn-ghost !px-2 !py-1" @click="addArray">{{ t('rules.addItem') }}</button>
      <button v-if="kind === 'object'" type="button" class="btn btn-ghost !px-2 !py-1" @click="addObject">{{ t('rules.addProperty') }}</button>
    </div>
    <textarea v-if="kind === 'string'" class="input" rows="2" :value="modelValue as string" @input="update(($event.target as HTMLTextAreaElement).value)" />
    <input v-else-if="kind === 'number'" class="input" type="number" :value="modelValue as number" step="any" required @input="updateNumber" />
    <label v-else-if="kind === 'boolean'" class="flex items-center gap-2 text-sm"><input type="checkbox" :checked="modelValue as boolean" @change="update(($event.target as HTMLInputElement).checked)" /> {{ String(modelValue) }}</label>
    <div v-else-if="kind === 'array'" class="space-y-2">
      <div v-for="(item, index) in modelValue as JsonValue[]" :key="index" class="flex items-start gap-2">
        <span class="pt-2 font-mono text-[11px] text-[color:var(--color-ink-faint)]">{{ index }}</span>
        <div class="min-w-0 flex-1"><JsonValueEditor :model-value="item" :path="`${path}/${index}`" :depth="depth + 1" @update:model-value="updateArray(index, $event)" /></div>
        <button type="button" class="btn btn-ghost !px-2 !py-1" :title="t('rules.remove')" @click="removeArray(index)">×</button>
      </div>
    </div>
    <div v-else-if="kind === 'object'" class="space-y-2">
      <div v-for="[key, item] in objectEntries" :key="key" class="flex items-start gap-2">
        <textarea class="input !w-36 !py-1 font-mono text-[12px]" rows="1" :value="key" :aria-label="t('rules.propertyName')" @change="changeKey(key, $event)" />
        <div class="min-w-0 flex-1"><JsonValueEditor :model-value="item" :path="`${path}/${key}`" :depth="depth + 1" @update:model-value="updateObject(key, $event)" /></div>
        <button type="button" class="btn btn-ghost !px-2 !py-1" :title="t('rules.remove')" @click="removeObject(key)">×</button>
      </div>
    </div>
  </div>
</template>
