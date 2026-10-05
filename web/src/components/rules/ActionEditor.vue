<script setup lang="ts">
import { computed } from 'vue'
import type { RuleAction, RuleFieldError, RuleSchema } from '../../api/rules'
import { useI18n } from '../../i18n'
import { localHelp, newAction } from '../../utils/ruleSchema'
import { moveItem } from '../../utils/ruleEditor'
import RuleField from './RuleField.vue'
const props = defineProps<{ modelValue: RuleAction[]; schema: RuleSchema; phase: string; errors?: RuleFieldError[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: RuleAction[]] }>()
const { t, locale } = useI18n()
const available = computed(() => props.schema.actions.filter(c => !c.phases?.length || c.phases.includes(props.phase as never)))
function set(value: RuleAction[]) { emit('update:modelValue', value) }
function update(index: number, key: string, value: unknown) { set(props.modelValue.map((a, i) => i === index ? {...a, [key]: value} as RuleAction : a)) }
function changeType(index: number, type: string) { set(props.modelValue.map((a, i) => i === index ? {...newAction(type, props.schema), id: a.id} : a)) }
function updateParam(index: number, name: string, value: unknown) { const action = props.modelValue[index]!; const params = {...action.params}; if (value === undefined) delete params[name]; else params[name] = value; update(index, 'params', params) }
function add(type: string) { set([...props.modelValue, newAction(type, props.schema)]) }
function remove(index: number) { set(props.modelValue.filter((_, i) => i !== index)) }
</script>
<template>
  <div class="space-y-3">
    <div v-for="(action, index) in modelValue" :key="action.id" class="rounded-lg border border-[color:var(--color-line)] p-3" :data-rule-path="`/actions/${index}`">
      <div class="flex flex-wrap items-start gap-2">
        <span class="rounded bg-[color:var(--color-panel-muted)] px-2 py-1 font-mono text-xs">{{ index + 1 }}</span>
        <select class="input min-w-44 flex-1" :value="action.type" @change="changeType(index, ($event.target as HTMLSelectElement).value)"><option v-for="cap in available" :key="cap.id" :value="cap.id">{{ cap.id }}</option></select>
        <button class="btn btn-ghost !px-2" type="button" :disabled="index === 0" :title="t('rules.up')" @click="set(moveItem(modelValue, index, -1))">↑</button>
        <button class="btn btn-ghost !px-2" type="button" :disabled="index === modelValue.length - 1" :title="t('rules.down')" @click="set(moveItem(modelValue, index, 1))">↓</button>
        <button class="btn btn-ghost !px-2" type="button" :title="t('rules.remove')" @click="remove(index)">×</button>
      </div>
      <label class="mt-2 block text-[11px]">{{ t('rules.actionID') }}<input class="input mt-1 font-mono" :value="action.id" required @change="update(index, 'id', ($event.target as HTMLInputElement).value)" /></label>
      <p v-for="error in (errors ?? []).filter(e => e.path === `/actions/${index}/id` || e.path === `/actions/${index}/type` || e.path === `/actions/${index}/params`)" :key="error.path" role="alert" class="text-xs text-red-500">{{ error.path }}: {{ error.message }}</p>
      <p class="mt-2 text-[11px] text-[color:var(--color-ink-muted)]">{{ localHelp(schema.actions.find(c => c.id === action.type)?.description, locale) }}</p>
      <div class="mt-3 grid gap-3 sm:grid-cols-2">
        <RuleField v-for="field in schema.actions.find(c => c.id === action.type)?.fields ?? []" :key="field.name" :class="['value', 'values', 'condition', 'condition_array'].includes(field.type) ? 'sm:col-span-2' : ''" :field="field" :model-value="action.params[field.name]" :schema="schema" :path="`/actions/${index}/params/${field.name}`" :errors="errors" @update:model-value="updateParam(index, field.name, $event)" />
      </div>
    </div>
    <div class="flex flex-wrap gap-2">
      <select id="add-rule-action" class="input max-w-xs" @change="add(($event.target as HTMLSelectElement).value); ($event.target as HTMLSelectElement).value = ''"><option value="">{{ t('rules.addAction') }}</option><option v-for="cap in available" :key="cap.id" :value="cap.id">{{ cap.id }}</option></select>
      <span v-if="!modelValue.length" class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.noActions') }}</span>
    </div>
  </div>
</template>
