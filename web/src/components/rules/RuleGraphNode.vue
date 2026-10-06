<script setup lang="ts">
import {computed, onBeforeUnmount, ref} from 'vue'
import {Handle, Position, type NodeProps} from '@vue-flow/core'
import type {Rule, RuleAction, RuleCondition, RuleFieldError, RulePhase, RuleSchema} from '../../api/rules'
import type {RuleGraphNode} from '../../utils/ruleGraph'
import {useI18n} from '../../i18n'
import {ruleLabel, ruleDisplayName} from '../../utils/ruleLabels'
import {localHelp} from '../../utils/ruleSchema'
import RuleNodeFields from './RuleNodeFields.vue'

type Data = {
    kind: 'rule' | 'condition' | 'action'
    path: string
    data: RuleGraphNode['data']
    schema: RuleSchema
    diagnostics?: Record<string, string>
    errors?: RuleFieldError[]
    sample?: unknown
    phase?: RulePhase
}
const props = defineProps<NodeProps<Data>>()
const emit = defineEmits<{
    'update:data': [value: RuleGraphNode['data']]
    'quick-add': [value: {kind: 'condition' | 'action'; mode?: 'auto' | 'wrap'}]
    advanced: []
    'edit-start': []
    'edit-end': []
}>()
const {locale, t} = useI18n()
const item = computed(() => props.data.data)
const group = computed(() => props.data.kind === 'condition' && ['all', 'any', 'not'].includes((item.value as RuleCondition).op))
const childCount = computed(() => (item.value as RuleCondition).conditions?.length ?? 0)
const title = computed(() => props.data.kind === 'rule' ? ruleDisplayName(item.value as Rule, locale.value) || t('rules.add') : props.data.kind === 'condition' ? ruleLabel('condition', (item.value as RuleCondition).op, locale.value) : ruleLabel('action', (item.value as RuleAction).type, locale.value))
const summary = computed(() => {
    if (props.data.kind === 'rule') return ruleLabel('phase', (item.value as Rule).phase, locale.value)
    if (props.data.kind === 'condition') return (item.value as RuleCondition).path ? String((item.value as RuleCondition).path) : t('rules.when')
    const action = item.value as RuleAction
    return String(action.params.path ?? action.params.model ?? localHelp(props.data.schema.actions.find(c => c.id === action.type)?.description, locale.value))
})
const status = computed(() => props.data.diagnostics?.[props.data.path])
const editing = ref(false)
function startEditing() {
    if (editing.value) return
    editing.value = true
    emit('edit-start')
}
function endEditing() {
    if (!editing.value) return
    editing.value = false
    emit('edit-end')
}
function focusOut(event: FocusEvent) {
    const area = event.currentTarget as HTMLElement
    if (event.relatedTarget instanceof Node && area.contains(event.relatedTarget)) return
    endEditing()
}
function keyboard(event: KeyboardEvent) {
    event.stopPropagation()
    // IME confirmation and textarea newlines must retain their native behavior.
    if (event.isComposing || event.keyCode === 229) return
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'd') event.preventDefault()
    const target = event.target as HTMLElement
    if (event.key === 'Enter' && !target.closest('textarea,button,summary,[contenteditable="true"]')) event.preventDefault()
}
onBeforeUnmount(endEditing)
</script>
<template>
  <div class="rule-graph-node" :class="`rule-graph-${data.kind}`" :data-node-id="id" :data-testid="`rule-node-${id}`">
    <Handle v-if="data.kind === 'rule' || group" id="boolean-in" type="target" :position="Position.Left" :title="t('rules.canvas.booleanPort')" />
    <Handle v-if="data.kind === 'action'" id="action-in" class="action-handle" type="target" :position="Position.Left" :title="t('rules.canvas.actionPort')" />
    <Handle v-if="data.kind === 'condition'" id="boolean-out" type="source" :position="Position.Right" />
    <Handle v-if="data.kind === 'rule' || data.kind === 'action'" id="action-out" type="source" :position="Position.Right" />
    <div class="rule-graph-node-title rule-node-drag-handle" :title="title">{{ title }}</div>
    <div class="rule-graph-node-summary">{{ summary }}</div>
    <div v-if="status" class="rule-graph-node-status">{{ status }}</div>
    <div
      class="rule-node-controls nodrag nopan nowheel"
      data-testid="node-inline"
      @pointerdown.stop
      @pointerup.stop
      @mousedown.stop
      @touchstart.stop
      @click.stop
      @dblclick.stop
      @wheel.stop
      @keydown="keyboard"
      @keyup.stop
      @focusin="startEditing"
      @focusout="focusOut"
    >
      <RuleNodeFields :node-id="id" :kind="data.kind" :path="data.path" :model-value="item" :schema="data.schema" :errors="data.errors" :sample="data.sample" @update:model-value="emit('update:data', $event)" />
      <div class="rule-node-actions">
        <button v-if="data.kind === 'rule' || group && ((item as RuleCondition).op !== 'not' || !childCount)" type="button" class="btn btn-ghost" data-testid="node-add-condition" @click="emit('quick-add', {kind: 'condition', mode: 'auto'})">{{ data.kind === 'rule' ? t('rules.addCondition') : t('rules.canvas.addChild') }}</button>
        <button v-if="data.kind === 'condition'" type="button" class="btn btn-ghost" data-testid="node-wrap-condition" @click="emit('quick-add', {kind: 'condition', mode: 'wrap'})">{{ t('rules.canvas.wrapCondition') }}</button>
        <button v-if="data.kind === 'rule' || data.kind === 'action'" type="button" class="btn btn-ghost" data-testid="node-add-action" @click="emit('quick-add', {kind: 'action', mode: 'auto'})">{{ t('rules.canvas.addAfter') }}</button>
        <button type="button" class="btn btn-ghost" data-testid="node-advanced" @click="emit('advanced')">{{ t('rules.canvas.inlineAdvanced') }}</button>
      </div>
    </div>
    <Handle v-if="data.kind === 'action' && (item as RuleAction).type === 'array_filter'" id="predicate:predicate" type="target" :position="Position.Bottom" :title="t('rules.canvas.predicatePort')" />
  </div>
</template>
<style scoped>
.rule-graph-node { position: relative; box-sizing: border-box; width: 300px; border: 1px solid var(--color-line); border-radius: .55rem; background: var(--color-surface-elevated, var(--color-surface, var(--color-canvas))); color: var(--color-ink); padding: .65rem .75rem; box-shadow: 0 2px 8px rgb(0 0 0 / 8%); }
.rule-graph-condition { border-color: color-mix(in srgb, var(--color-accent) 45%, var(--color-line)); }
.rule-graph-action { border-color: color-mix(in srgb, #d97706 45%, var(--color-line)); }
.rule-graph-rule { border-color: var(--color-accent); }
.rule-graph-node-title { font-size: .78rem; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rule-node-drag-handle { cursor: grab; padding-bottom: .15rem; }
.rule-node-drag-handle:active { cursor: grabbing; }
.rule-graph-node-summary { margin-top: .2rem; color: var(--color-ink-muted); font-size: .67rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rule-graph-node-status { margin-top: .35rem; color: var(--color-accent); font-size: .65rem; }
.rule-node-controls { margin-top: .55rem; padding-top: .5rem; border-top: 1px solid var(--color-line); cursor: default; }
.rule-node-actions { display: flex; flex-wrap: wrap; gap: .3rem; margin-top: .55rem; }
.rule-node-actions .btn { padding: .3rem .4rem; font-size: .66rem; }
</style>
