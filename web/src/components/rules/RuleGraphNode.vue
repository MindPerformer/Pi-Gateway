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
    <div class="rule-node-header rule-node-drag-handle" :title="summary">
      <span class="rule-node-kind">{{ data.kind === 'rule' ? t('rules.canvas.rule') : data.kind === 'condition' ? t('rules.when') : t('rules.canvas.action') }}</span>
      <span class="rule-graph-node-title">{{ title }}</span>
    </div>
    <div class="rule-node-ports">
      <span>{{ data.kind === 'action' ? t('rules.canvas.actionPort') : data.kind === 'rule' || group ? t('rules.canvas.booleanPort') : '' }}</span>
      <span>{{ data.kind === 'condition' ? t('rules.canvas.booleanPort') : t('rules.canvas.actionPort') }}</span>
    </div>

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
      <RuleNodeFields :node-id="id" :kind="data.kind" :path="data.path" :model-value="item" :schema="data.schema" :errors="data.errors" :sample="data.sample" :phase="data.phase" @update:model-value="emit('update:data', $event)" />
      <div class="rule-node-actions">
        <button v-if="data.kind === 'rule' || group && ((item as RuleCondition).op !== 'not' || !childCount)" type="button" class="btn btn-ghost" data-testid="node-add-condition" @click="emit('quick-add', {kind: 'condition', mode: 'auto'})">{{ data.kind === 'rule' ? t('rules.addCondition') : t('rules.canvas.addChild') }}</button>
        <button v-if="data.kind === 'condition'" type="button" class="btn btn-ghost" data-testid="node-wrap-condition" @click="emit('quick-add', {kind: 'condition', mode: 'wrap'})">{{ t('rules.canvas.wrapCondition') }}</button>
        <button v-if="data.kind === 'rule' || data.kind === 'action'" type="button" class="btn btn-ghost" data-testid="node-add-action" @click="emit('quick-add', {kind: 'action', mode: 'auto'})">{{ t('rules.canvas.addAfter') }}</button>
      </div>
    </div>
    <Handle v-if="data.kind === 'action' && (item as RuleAction).type === 'array_filter'" id="predicate:predicate" type="target" :position="Position.Bottom" :title="t('rules.canvas.predicatePort')" />
  </div>
</template>
<style scoped>
.rule-graph-node { --node-tint: var(--color-accent); position: relative; box-sizing: border-box; width: 400px; border: 1px solid var(--color-line-strong); border-radius: 10px; background: var(--color-surface-elevated); color: var(--color-ink); box-shadow: 0 4px 18px rgb(0 0 0 / 12%); }
.rule-graph-condition { --node-tint: #818cf8; }
.rule-graph-action { --node-tint: #d29958; }
.rule-node-header { display: flex; align-items: center; gap: 10px; height: 42px; padding: 0 16px; border-radius: 9px 9px 0 0; border-bottom: 1px solid var(--color-line); background: color-mix(in srgb, var(--node-tint) 14%, var(--color-surface-elevated)); }
.rule-node-kind { flex-shrink: 0; font-size: 10px; color: var(--color-ink-muted); }
.rule-graph-node-title { flex: 1; min-width: 0; font-size: 13px; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rule-node-drag-handle { cursor: grab; }
.rule-node-drag-handle:active { cursor: grabbing; }
.rule-node-ports { display: flex; justify-content: space-between; gap: 16px; padding: 9px 16px; color: var(--color-ink-muted); font-size: 10px; border-bottom: 1px solid var(--color-line); }
.rule-graph-node-status { margin: 8px 12px 0; border-radius: 4px; padding: 4px 8px; background: var(--color-accent-soft); color: var(--color-accent); font-size: 11px; }
.rule-node-controls { padding: 10px 12px 12px; cursor: default; }
.rule-node-actions { display: flex; flex-wrap: wrap; gap: 6px; padding-top: 10px; margin-top: 10px; border-top: 1px solid var(--color-line); }
.rule-node-actions .btn { flex: 1; justify-content: center; padding: 5px 8px; font-size: 11px; min-height: 28px; background: var(--color-surface-2); }
:deep(.vue-flow__handle) { top: 60px; }
:deep(.vue-flow__handle-bottom) { top: auto; }
</style>
