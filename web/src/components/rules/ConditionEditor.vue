<script setup lang="ts">
import { computed } from 'vue'
import type { RuleCondition, RuleFieldError, RuleSchema } from '../../api/rules'
import { useI18n } from '../../i18n'
import { defaultCondition, moveItem } from '../../utils/ruleEditor'
import { localHelp } from '../../utils/ruleSchema'
import { ruleLabel } from '../../utils/ruleLabels'
import { currentSamplePathField } from '../../utils/samplePaths'
import RuleField from './RuleField.vue'
const props = withDefaults(defineProps<{ modelValue: RuleCondition; schema: RuleSchema; path?: string; errors?: RuleFieldError[]; compact?: boolean; sample?: unknown; idPrefix?: string }>(), { path: '/when' })
const emit = defineEmits<{ 'update:modelValue': [value: RuleCondition] }>()
const { t, locale } = useI18n()
const cap = computed(() => props.schema.conditions.find(c => c.id === props.modelValue.op))
const group = computed(() => ['all', 'any', 'not'].includes(props.modelValue.op))
const children = computed(() => props.modelValue.conditions ?? [])
const visibleFields = computed(() => cap.value?.fields ?? [])
function field(name: string, value: unknown) { const next = {...props.modelValue} as Record<string, unknown>; if (value === undefined) delete next[name]; else next[name] = value; emit('update:modelValue', next as unknown as RuleCondition) }
function updateChild(index: number, value: RuleCondition) { field('conditions', children.value.map((c, i) => i === index ? value : c)) }
</script>
<template>
  <div class="rule-condition-card space-y-3 rounded-lg border border-[color:var(--color-line)] p-3" :class="{'is-compact': compact}" :data-rule-path="path" data-renderer="condition">
    <div class="flex flex-wrap items-start gap-2">
      <label class="min-w-0 flex-1 text-[11px] text-[color:var(--color-ink-muted)]">{{ t('rules.conditionType') }}
        <select class="input mt-1" :value="modelValue.op" @change="emit('update:modelValue', defaultCondition(($event.target as HTMLSelectElement).value, schema))"><option v-for="item in schema.conditions" :key="item.id" :value="item.id">{{ ruleLabel('condition', item.id, locale) }}</option></select>
      </label>
      <p class="min-w-0 flex-[2] pt-4 text-[11px] text-[color:var(--color-ink-muted)]">{{ localHelp(cap?.description, locale) }}</p>
    </div>
    <template v-if="group">
      <div v-for="(child, index) in children" :key="index" class="flex items-start gap-1">
        <ConditionEditor class="min-w-0 flex-1" :model-value="child" :schema="schema" :path="`${path}/conditions/${index}`" :errors="errors" :sample="sample" :compact="compact" :id-prefix="idPrefix ? `${idPrefix}-${index}` : undefined" @update:model-value="updateChild(index, $event)" />
        <div v-if="modelValue.op !== 'not'" class="flex flex-col gap-1">
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === 0" :title="t('rules.up')" @click="field('conditions', moveItem(children, index, -1))">↑</button>
          <button type="button" class="btn btn-ghost !px-2" :disabled="index === children.length - 1" :title="t('rules.down')" @click="field('conditions', moveItem(children, index, 1))">↓</button>
          <button type="button" class="btn btn-ghost !px-2" :title="t('rules.remove')" @click="field('conditions', children.filter((_, i) => i !== index))">×</button>
        </div>
      </div>
      <button v-if="modelValue.op !== 'not' || !children.length" type="button" class="btn" @click="field('conditions', [...children, {op:'always'}])">{{ t('rules.addCondition') }}</button>
    </template>
    <div v-else class="rule-condition-fields">
      <RuleField v-for="f in visibleFields" :key="f.name" :compact="compact" :id-prefix="idPrefix" :field="f" :model-value="(modelValue as unknown as Record<string, unknown>)[f.name]" :schema="schema" :path="`${path}/${f.name}`" :errors="errors" :sample="currentSamplePathField(f, modelValue.source) ? sample : undefined" @update:model-value="field(f.name, $event)" />
    </div>
    <p v-for="error in (errors ?? []).filter(e => e.path === path || e.path === `${path}/op` || e.path === `${path}/conditions`)" :key="error.path + error.message" role="alert" class="text-xs text-red-500">{{ error.path }}: {{ error.message }}</p>
  </div>
</template>

<style scoped>
.rule-condition-card { min-width: 0; }
.rule-condition-fields { display: grid; gap: 10px; }
.rule-condition-card.is-compact { padding: 8px; background: var(--color-surface); }
.rule-condition-card.is-compact > div:first-child { display: block; }
.rule-condition-card.is-compact > div:first-child > p { display: none; }
</style>
