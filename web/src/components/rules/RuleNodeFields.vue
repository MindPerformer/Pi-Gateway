<script setup lang="ts">
import {computed} from 'vue'
import type {RuleAction, RuleCondition, RuleField, RuleFieldError, RuleSchema} from '../../api/rules'
import type {RuleGraphNode} from '../../utils/ruleGraph'
import {pointerPart} from '../../utils/ruleEditor'
import {currentSamplePathField} from '../../utils/samplePaths'
import {ruleLabel} from '../../utils/ruleLabels'
import {useI18n} from '../../i18n'
import RuleFieldEditor from './RuleField.vue'

const props = defineProps<{
  nodeId: string
  kind: RuleGraphNode['kind']
  path: string
  modelValue: RuleGraphNode['data']
  schema: RuleSchema
  errors?: RuleFieldError[]
  sample?: unknown
}>()
const emit = defineEmits<{'update:modelValue': [value: RuleGraphNode['data']]}>()
const {t, locale} = useI18n()
const group = computed(() => props.kind === 'condition' && ['all', 'any', 'not'].includes((props.modelValue as RuleCondition).op))
const values = computed<Record<string, unknown>>(() => props.kind === 'action' ? (props.modelValue as RuleAction).params : props.modelValue as unknown as Record<string, unknown>)
const fields = computed(() => {
  if (props.kind === 'rule') {
    return ['phase', 'enabled', 'priority', 'stop_after_match', 'on_error']
      .flatMap(name => props.schema.rule_fields.filter(field => field.name === name))
  }
  if (group.value) return []
  const registered = props.kind === 'condition'
    ? props.schema.conditions.find(cap => cap.id === (props.modelValue as RuleCondition).op)?.fields ?? []
    : props.schema.actions.find(cap => cap.id === (props.modelValue as RuleAction).type)?.fields ?? []
  // Nested conditions have independent graph nodes, never duplicate their editors.
  const editable = registered.filter(field => !['condition', 'condition_array', 'action_array'].includes(field.type) && !['predicate', 'conditions'].includes(field.name))
  const common = props.kind === 'condition'
    ? ['source', 'path', 'value', 'pattern']
    : ['path', 'model', 'paths', 'source', 'source_path', 'target_path', 'pattern', 'replacement', 'mode']
  const chosen = editable.filter(field => field.required || common.includes(field.name))
  // Optional-only capabilities still provide a small useful editing surface.
  return chosen.length ? chosen : editable.slice(0, 2)
})
const childCount = computed(() => (props.modelValue as RuleCondition).conditions?.length ?? 0)
const predicate = computed(() => props.kind === 'action' && (props.modelValue as RuleAction).type === 'array_filter' ? values.value.predicate as RuleCondition | undefined : undefined)
const fieldPath = (field: RuleField) => `${props.path}${props.kind === 'action' ? '/params' : ''}/${pointerPart(field.name)}`
const idPrefix = computed(() => `node-${encodeURIComponent(props.nodeId)}`)
function fieldSample(field: RuleField) {
  const source = props.kind === 'action' && field.name === 'target_path' ? 'current' : values.value.source
  return props.kind !== 'rule' && currentSamplePathField(field, source) ? props.sample : undefined
}
function updateField(field: RuleField, value: unknown) {
  // modelValue is the full graphNodeValue, so predicate/group subtrees survive.
  const next = {...values.value}
  if (value === undefined) delete next[field.name]
  else next[field.name] = value
  emit('update:modelValue', props.kind === 'action'
    ? {...props.modelValue as RuleAction, params: next}
    : next as unknown as RuleGraphNode['data'])
}
const nodeErrors = computed(() => (props.errors ?? []).filter(error => {
  if (fields.value.some(field => error.path === fieldPath(field) || error.path.startsWith(`${fieldPath(field)}/`))) return false
  if (error.path === props.path) return true
  if (props.kind === 'rule') return error.path.startsWith(`${props.path}/`) && !error.path.slice(props.path.length + 1).includes('/') && !['/when', '/actions'].includes(error.path)
  if (props.kind === 'condition') return error.path === `${props.path}/op` || error.path === `${props.path}/conditions`
  return error.path.startsWith(`${props.path}/`) && !error.path.startsWith(`${props.path}/params/predicate/`)
}))
</script>
<template>
  <div class="rule-node-fields" :data-testid="`rule-node-fields-${nodeId}`">
    <p v-if="group" class="rule-node-field-summary" data-testid="rule-node-condition-count">{{ ruleLabel('field', 'conditions', locale) }}: {{ childCount }}</p>
    <template v-else>
      <div v-if="fields.length" class="rule-node-inline-heading">{{ t('rules.canvas.inlineParameters') }}</div>
      <RuleFieldEditor
        v-for="field in fields"
        :key="field.name"
        :field="field"
        :model-value="values[field.name]"
        :schema="schema"
        :path="fieldPath(field)"
        :errors="errors"
        :sample="fieldSample(field)"
        compact
        :id-prefix="idPrefix"
        @update:model-value="updateField(field, $event)"
      />
    </template>
    <p v-if="kind === 'action' && (modelValue as RuleAction).type === 'array_filter'" class="rule-node-field-summary" data-testid="rule-node-predicate-summary">
      {{ ruleLabel('field', 'predicate', locale) }}: {{ predicate ? ruleLabel('condition', predicate.op, locale) : t('rules.none') }}<template v-if="predicate?.conditions"> · {{ ruleLabel('field', 'conditions', locale) }}: {{ predicate.conditions.length }}</template>
    </p>
    <p v-for="error in nodeErrors" :key="error.path + error.message" class="rule-node-field-error" role="alert">{{ error.path }}: {{ error.message }}</p>
  </div>
</template>
<style scoped>
.rule-node-fields { display: grid; gap: .5rem; min-width: 0; }
.rule-node-inline-heading { color: var(--color-ink-muted); font-size: .64rem; font-weight: 650; }
.rule-node-field-summary { color: var(--color-ink-muted); font-size: .68rem; overflow-wrap: anywhere; }
.rule-node-field-error { color: #ef4444; font-size: .65rem; overflow-wrap: anywhere; }
</style>
