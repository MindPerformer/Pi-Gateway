<script setup lang="ts">
import {computed, nextTick, ref, onBeforeUnmount} from 'vue'
import { Plus, Search, ZoomIn, ZoomOut, Maximize, LayoutGrid, Undo2, Redo2, Copy, Trash2 } from 'lucide-vue-next'
import {VueFlow, useVueFlow, MarkerType, type Connection, type NodeChange, type EdgeChange} from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import type {Rule, RuleAction, RuleCondition, RuleFieldError, RuleSchema} from '../../api/rules'
import {useI18n} from '../../i18n'
import {clone, RuleInputError} from '../../utils/ruleEditor'
import {editorId} from '../../utils/editorId'
import {localHelp} from '../../utils/ruleSchema'
import {ruleLabel, ruleDisplayName} from '../../utils/ruleLabels'
import {ruleNodeWidth, addGraphModule, connectGraph, graphNodeValue, graphPathMap, type RuleGraph, type RuleGraphNode, type RuleGraphEdge, type RuleGraphAddOptions} from '../../utils/ruleGraph'
import RuleCanvasNode from './RuleGraphNode.vue'
import RuleNodeSearch from './RuleNodeSearch.vue'

const props = defineProps<{modelValue: RuleGraph; schema: RuleSchema; diagnostics?: Record<string, string>; errors?: RuleFieldError[]; sample?: unknown; canUndo?: boolean; canRedo?: boolean}>()
const emit = defineEmits<{
    'update:modelValue': [value: RuleGraph]
    undo: []
    redo: []
    select: [id: string]
    edit: [id: string, value: RuleGraphNode['data']]
    'edit-start': [id: string]
    'edit-end': [id: string]
}>()
const {t, locale} = useI18n()
const flowId = editorId()
const flow = useVueFlow(flowId)
const canvasElement = ref<HTMLElement>()
const search = ref(''), libraryOpen = ref(false), connectionError = ref('')
const pathMap = computed(() => graphPathMap(props.modelValue))
const root = computed(() => props.modelValue.nodes.find(n => n.kind === 'rule')!)
const selected = computed(() => props.modelValue.nodes.find(n => props.modelValue.selected.includes(n.id)))
const phase = computed(() => (root.value.data as Rule).phase)
const conditions = computed(() => props.schema.conditions.filter(c => (ruleLabel('condition', c.id, locale.value) + c.id + localHelp(c.description, locale.value)).toLowerCase().includes(search.value.toLowerCase())))
const actions = computed(() => props.schema.actions.filter(c => (!c.phases?.length || c.phases.includes(phase.value)) && (ruleLabel('action', c.id, locale.value) + c.id + localHelp(c.description, locale.value)).toLowerCase().includes(search.value.toLowerCase())))
const nodes = computed(() => props.modelValue.nodes.map(node => ({
    id: node.id, type: 'rule-module', position: node.position, dragHandle: '.rule-node-drag-handle',
    data: {...node, data: graphNodeValue(props.modelValue, node.id) ?? node.data, path: pathMap.value[node.id] ?? node.path, schema: props.schema, diagnostics: props.diagnostics, errors: props.errors, sample: props.sample, phase: phase.value},
    selected: props.modelValue.selected.includes(node.id), deletable: node.kind !== 'rule',
    ariaLabel: node.kind === 'rule' ? t('rules.canvas.rule') : ruleLabel(node.kind, node.kind === 'condition' ? (node.data as RuleCondition).op : (node.data as RuleAction).type, locale.value),
})))
const edges = computed(() => props.modelValue.edges.map(edge => ({...edge, markerEnd: MarkerType.ArrowClosed, style: {stroke: edge.sourceHandle === 'boolean-out' ? 'var(--color-accent)' : '#d97706', strokeWidth: 2}, label: edge.sourceHandle === 'boolean-out' ? String(edge.order + 1) : undefined})))
function publish(graph: RuleGraph) { emit('update:modelValue', graph) }
function select(id: string) { publish({...props.modelValue, selected: [id]}); emit('select', id) }
function changes(changes: NodeChange[]) {
    flow.applyNodeChanges(changes)
    let graph = props.modelValue
    let changed = false
    for (const change of changes) {
        if (change.type === 'position' && change.position) { graph = {...graph, nodes: graph.nodes.map(n => n.id === change.id ? {...n, position: change.position!} : n)}; changed = true }
        if (change.type === 'select') { graph = {...graph, selected: change.selected ? [...new Set([...graph.selected, change.id])] : graph.selected.filter(id => id !== change.id)}; changed = true }
        if (change.type === 'remove' && graph.nodes.find(n => n.id === change.id)?.kind !== 'rule') { graph = {...graph, nodes: graph.nodes.filter(n => n.id !== change.id), edges: graph.edges.filter(e => e.source !== change.id && e.target !== change.id)}; changed = true }
    }
    if (changed) publish(graph)
    if (changes.some(change => change.type === 'dimensions')) scheduleSeparation()
}
function edgeChanges(changes: EdgeChange[]) {
    flow.applyEdgeChanges(changes)
    const removed = changes.filter(c => c.type === 'remove').map(c => c.id)
    if (removed.length) publish({...props.modelValue, edges: props.modelValue.edges.filter(e => !removed.includes(e.id))})
}
function connection(value: Connection): Omit<RuleGraphEdge, 'id'|'order'> {
    return {...value, sourceHandle: value.sourceHandle as RuleGraphEdge['sourceHandle'], targetHandle: value.targetHandle ?? ''}
}
function validConnection(value: Connection): boolean {
    if (props.modelValue.edges.some(edge => edge.source === value.source && edge.target === value.target && edge.sourceHandle === value.sourceHandle && edge.targetHandle === value.targetHandle)) return true
    try { connectGraph(props.modelValue, connection(value), props.schema); return true } catch { return false }
}
function reportConnectionError(error: unknown) {
    const code = error instanceof RuleInputError ? error.errors[0]?.code : undefined
    connectionError.value = t(code?.startsWith('graph.') ? `rules.${code}` : 'rules.graph.invalid')
}
function connect(value: Connection) {
    connectionCompleted = true
    try { publish(connectGraph(props.modelValue, connection(value), props.schema)); connectionError.value = '' }
    catch (error) { reportConnectionError(error) }
}

type QuickRequest = {kind: 'condition' | 'action'; mode?: 'auto' | 'wrap'}
type QuickState = {screen: {x: number; y: number}; position?: {x: number; y: number}; targetId?: string; kind?: 'condition' | 'action'; mode?: 'auto' | 'wrap'}
const quick = ref<QuickState | null>(null)
const quickContext = computed(() => {
    if (!quick.value?.targetId) return t('rules.canvas.addContextDefault')
    const node = props.modelValue.nodes.find(node => node.id === quick.value!.targetId)
    if (!node) return t('rules.canvas.addContextDefault')
    const name = node.kind === 'rule' ? ruleDisplayName(node.data as Rule, locale.value) || t('rules.canvas.rule') : ruleLabel(node.kind, node.kind === 'condition' ? (node.data as RuleCondition).op : (node.data as RuleAction).type, locale.value)
    if (!quick.value.kind) return t(node.kind === 'action' ? 'rules.canvas.addMixedActionContext' : node.kind === 'condition' ? 'rules.canvas.addMixedConditionContext' : 'rules.canvas.addMixedRootContext', {name})
    return t(quick.value.mode === 'wrap' ? 'rules.canvas.wrapContext' : quick.value.kind === 'action' ? 'rules.canvas.addAfterContext' : 'rules.canvas.addConditionContext', {name})
})
function centerPoint() {
    const bounds = canvasElement.value?.getBoundingClientRect()
    return bounds ? {x: bounds.left + bounds.width / 2, y: bounds.top + Math.min(160, bounds.height / 2)} : {x: 40, y: 100}
}
function openSearch(point = centerPoint()) {
    quick.value = {screen: point, position: flow.screenToFlowCoordinate(point), targetId: selected.value?.id}
    connectionError.value = ''
}
function isCanvasBackground(target: EventTarget | null) {
    return target instanceof Element && !!canvasElement.value?.contains(target) && !target.closest('.vue-flow__node, .vue-flow__edge, .vue-flow__handle, button, input, textarea, select')
}
function canvasDoubleClick(event: MouseEvent) {
    if (!isCanvasBackground(event.target)) return
    event.preventDefault()
    openSearch({x: event.clientX, y: event.clientY})
}
function canvasContextMenu(event: MouseEvent) {
    event.preventDefault()
    openSearch({x: event.clientX, y: event.clientY})
}
function openNodeQuickAdd(id: string, request: QuickRequest) {
    const element = canvasElement.value?.querySelector(`[data-node-id="${CSS.escape(id)}"]`)
    const bounds = element?.getBoundingClientRect()
    quick.value = {targetId: id, kind: request.kind, mode: request.mode ?? 'auto', screen: bounds ? {x: bounds.left + 30, y: bounds.bottom - 25} : centerPoint()}
    connectionError.value = ''
}
function closeSearch() { quick.value = null; canvasElement.value?.focus({preventScroll: true}) }
async function createModule(options: RuleGraphAddOptions) {
    try {
        let next: RuleGraph
        let detached = false
        try { next = addGraphModule(props.modelValue, options, props.schema) }
        catch (error) {
            const code = error instanceof RuleInputError ? error.errors[0]?.code : undefined
            if ((options.mode === undefined || options.mode === 'auto') && (code === 'graph.target' || code === 'graph.orphan')) {
                next = addGraphModule(props.modelValue, {...options, targetId: undefined, mode: 'detached'}, props.schema)
                detached = true
            } else throw error
        }
        publish(next)
        connectionError.value = detached ? t('rules.graph.orphan') : ''
        quick.value = null
        libraryOpen.value = false
        const id = next.selected[0]
        if (id) {
            emit('select', id)
            await nextTick()
            await revealAdded(id)
        }
    } catch (error) { reportConnectionError(error) }
}
function chooseQuick(value: {kind: 'condition' | 'action'; type: string}) {
    if (!quick.value) return
    const node = props.modelValue.nodes.find(node => node.id === quick.value!.targetId)
    const targetId = value.kind === 'action' && node?.kind === 'condition' || value.kind === 'condition' && node?.kind === 'action' && !quick.value.kind ? undefined : quick.value.targetId
    void createModule({...value, targetId, position: quick.value.position, mode: quick.value.mode ?? 'auto'})
}
function libraryAdd(kind: 'condition' | 'action', type: string) {
    const wrap = kind === 'condition' && ['all', 'any', 'not'].includes(type)
    const targetId = kind === 'action' ? selected.value?.kind === 'action' || selected.value?.kind === 'rule' ? selected.value.id : undefined : selected.value?.kind === 'condition' ? selected.value.id : undefined
    void createModule({kind, type, targetId, mode: wrap ? 'wrap' : 'auto'})
}
let connecting: {nodeId: string | null; handleId: string | null; handleType: string | null} | undefined
let connectionCompleted = false
function startConnection(value: {nodeId?: string | null; handleId?: string | null; handleType?: string | null}) {
    connecting = {nodeId: value.nodeId ?? null, handleId: value.handleId ?? null, handleType: value.handleType ?? null}
    connectionCompleted = false
}
function endConnection(event?: MouseEvent | TouchEvent) {
    const pending = connecting
    connecting = undefined
    if (!pending?.nodeId || !event || connectionCompleted || !isCanvasBackground(event.target)) return
    const point = 'changedTouches' in event ? event.changedTouches[0] : event
    if (!point) return
    let targetId = pending.nodeId
    let request: QuickRequest
    if (pending.handleId === 'action-out') request = {kind: 'action'}
    else if (pending.handleId === 'action-in') {
        const prior = props.modelValue.edges.find(edge => edge.target === targetId && edge.targetHandle === 'action-in')
        if (!prior) return
        targetId = prior.source
        request = {kind: 'action'}
    } else if (pending.handleId === 'boolean-out') request = {kind: 'condition', mode: 'wrap'}
    else if (pending.handleId === 'boolean-in' || pending.handleId === 'predicate:predicate') request = {kind: 'condition'}
    else return
    const screen = {x: point.clientX, y: point.clientY}
    quick.value = {...request, targetId, screen, position: flow.screenToFlowCoordinate(screen)}
}
function remove() {
    const removed = props.modelValue.selected.filter(id => id !== root.value.id)
    publish({...props.modelValue, nodes: props.modelValue.nodes.filter(n => !removed.includes(n.id)), edges: props.modelValue.edges.filter(e => !removed.includes(e.source) && !removed.includes(e.target)), selected: []})
}
function duplicate() {
    const graph = clone(props.modelValue), included = new Set(graph.selected.filter(id => id !== root.value.id)), ids = new Map<string, string>()
    const collect = (id: string) => { for (const edge of graph.edges.filter(e => e.target === id && e.sourceHandle === 'boolean-out')) if (!included.has(edge.source)) { included.add(edge.source); collect(edge.source) } }
    for (const id of [...included]) collect(id)
    const originals = graph.nodes.filter(n => included.has(n.id))
    for (const item of originals) ids.set(item.id, editorId())
    for (const item of originals) { const node = clone(item); node.id = ids.get(item.id)!; node.position = {x: node.position.x + 60, y: node.position.y + 320}; if (node.kind === 'action') (node.data as RuleAction).id = editorId(); graph.nodes.push(node) }
    graph.edges.push(...graph.edges.filter(e => ids.has(e.source) && ids.has(e.target)).map(e => ({...e, id: editorId(), source: ids.get(e.source)!, target: ids.get(e.target)!})))
    graph.selected = [...ids.values()]; publish(graph)
}
// Programmatic viewport changes do not always emit Vue Flow's move-end event.
async function persistViewport() { await nextTick(); publish({...props.modelValue, layoutRestored: true, viewport: {...flow.getViewport()}}) }
async function fit() { await nextTick(); await flow.fitView({padding: 0.12, maxZoom: 1}); await persistViewport() }
async function zoom(direction: 'in' | 'out') { if (direction === 'in') await flow.zoomIn(); else await flow.zoomOut(); await persistViewport() }
async function revealAdded(id: string) {
    const node = flow.findNode(id)
    const bounds = canvasElement.value?.getBoundingClientRect()
    if (!node || !bounds) return
    const point = flow.flowToScreenCoordinate(node.position)
    const viewport = flow.getViewport()
    const width = node.dimensions.width || ruleNodeWidth, height = node.dimensions.height || 240
    if (point.x >= bounds.left + 12 && point.y >= bounds.top + 12 && point.x + width * viewport.zoom <= bounds.right - 12 && point.y + height * viewport.zoom <= bounds.bottom - 12) return
    await flow.setCenter(node.position.x + width / 2, node.position.y + height / 2, {zoom: viewport.zoom})
    await persistViewport()
}
function layout() {
    const graph = clone(props.modelValue)
    const cache = new Map<string, number>()
    const depth = (id: string, seen = new Set<string>()): number => {
        if (seen.has(id) || seen.size > 128) return 0
        if (cache.has(id)) return cache.get(id)!
        seen.add(id)
        const value = Math.max(0, ...graph.edges.filter(edge => edge.target === id).map(edge => 1 + depth(edge.source, new Set(seen))))
        cache.set(id, value)
        return value
    }
    const widths = new Map<number, number>()
    for (const node of graph.nodes) {
        const column = depth(node.id)
        widths.set(column, Math.max(widths.get(column) ?? ruleNodeWidth, flow.findNode(node.id)?.dimensions.width ?? ruleNodeWidth))
    }
    const columns = new Map<number, number>()
    let offset = 40
    for (let column = 0; column <= Math.max(0, ...widths.keys()); column++) {
        columns.set(column, offset)
        offset += (widths.get(column) ?? ruleNodeWidth) + 100
    }
    const heights = new Map<number, number>()
    for (const node of graph.nodes) {
        const column = depth(node.id), y = heights.get(column) ?? 40
        node.position = {x: columns.get(column)!, y}
        heights.set(column, y + Math.max(200, flow.findNode(node.id)?.dimensions.height ?? 0) + 70)
    }
    publish(graph); void fit()
}
// ResizeObserver updates from Vue Flow include nested editors and sample path widgets.
// Move only overlapping cards, leaving manual placement and cached viewports intact.
let separationFrame = 0
function scheduleSeparation() {
    cancelAnimationFrame(separationFrame)
    separationFrame = requestAnimationFrame(() => { separationFrame = 0; separateOverlaps() })
}
function separateOverlaps() {
    const graph = clone(props.modelValue)
    const placed: {x: number; y: number; width: number; height: number}[] = []
    let changed = false
    for (const node of graph.nodes) {
        const measured = flow.findNode(node.id)?.dimensions
        if (!measured?.height || !measured.width) continue
        let y = node.position.y
        const x = node.position.x, width = measured.width, height = measured.height
        for (let attempt = 0; attempt < graph.nodes.length; attempt++) {
            const conflicts = placed.filter(other => x < other.x + other.width + 30 && x + width + 30 > other.x && y < other.y + other.height + 40 && y + height + 40 > other.y)
            if (!conflicts.length) break
            y = Math.max(...conflicts.map(other => other.y + other.height + 40))
        }
        if (y !== node.position.y) { node.position.y = y; changed = true }
        placed.push({x, y, width, height})
    }
    if (changed) publish(graph)
}
let initialized = false
async function initializeLayout() {
    if (initialized) return
    initialized = true
    await nextTick()
    separateOverlaps()
    if (!props.modelValue.layoutRestored) await fit()
}
onBeforeUnmount(() => cancelAnimationFrame(separationFrame))
async function focus(id: string) {
    select(id)
    await nextTick()
    await flow.fitView({nodes: [id], padding: 0.7, maxZoom: 1.2})
    await persistViewport()
}
function keyboard(event: KeyboardEvent) {
    if (event.isComposing || (event.target as HTMLElement).closest?.('input,textarea,select,button,a,[contenteditable=true]')) return
    if (event.key === ' ' && !event.ctrlKey && !event.metaKey && !event.altKey) { event.preventDefault(); openSearch(); return }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') { event.preventDefault(); if (event.shiftKey) emit('redo'); else emit('undo') }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'd') { event.preventDefault(); duplicate() }
}
defineExpose({focus, fit})
</script>
<template>
    <div class="rule-canvas-shell" @keydown="keyboard">
        <div class="rule-canvas-toolbar">
            <div class="rule-toolbar-group">
                <button type="button" class="btn btn-primary" data-testid="canvas-add-node" @click="openSearch()"><Plus />{{ t('rules.canvas.addNode') }}</button>
                <button type="button" class="btn" data-testid="toggle-module-library" :aria-expanded="libraryOpen" @click="libraryOpen = !libraryOpen"><Search />{{ t('rules.canvas.library') }}</button>
            </div>
            <div class="rule-toolbar-group">
                <button type="button" class="btn" data-testid="canvas-zoom-in" :title="t('rules.canvas.zoomIn')" :aria-label="t('rules.canvas.zoomIn')" @click="zoom('in')"><ZoomIn /></button>
                <button type="button" class="btn" data-testid="canvas-zoom-out" :title="t('rules.canvas.zoomOut')" :aria-label="t('rules.canvas.zoomOut')" @click="zoom('out')"><ZoomOut /></button>
                <button type="button" class="btn" data-testid="canvas-fit" @click="fit"><Maximize />{{ t('rules.canvas.fit') }}</button>
                <button type="button" class="btn" @click="layout"><LayoutGrid />{{ t('rules.canvas.layout') }}</button>
            </div>
            <div class="rule-toolbar-group">
                <button type="button" class="btn" :disabled="!canUndo" @click="emit('undo')"><Undo2 />{{ t('rules.canvas.undo') }}</button>
                <button type="button" class="btn" :disabled="!canRedo" @click="emit('redo')"><Redo2 />{{ t('rules.canvas.redo') }}</button>
                <button type="button" class="btn" :disabled="!selected || selected.kind === 'rule'" @click="duplicate"><Copy />{{ t('rules.copy') }}</button>
                <button type="button" class="btn" :disabled="!selected || selected.kind === 'rule'" @click="remove"><Trash2 />{{ t('rules.remove') }}</button>
            </div>
        </div>
        <p class="rule-canvas-hint">{{ t('rules.canvas.inlineHint') }}</p>
        <div class="rule-canvas-body">
            <aside class="rule-module-library" :class="{'is-open': libraryOpen}" data-testid="module-library">
                <button type="button" class="btn library-close" @click="libraryOpen = false">{{ t('common.close') }}</button>
                <input v-model="search" class="input" :placeholder="t('rules.canvas.search')" :aria-label="t('rules.canvas.search')" />
                <h3>{{ t('rules.addCondition') }}</h3>
                <button v-for="cap in conditions" :key="cap.id" type="button" class="module-item" :data-testid="`add-condition-${cap.id}`" :title="localHelp(cap.description, locale)" @click="libraryAdd('condition', cap.id)">{{ ruleLabel('condition', cap.id, locale) }}</button>
                <h3>{{ t('rules.addAction') }}</h3>
                <button v-for="cap in actions" :key="cap.id" type="button" class="module-item" :data-testid="`add-action-${cap.id}`" :title="localHelp(cap.description, locale)" @click="libraryAdd('action', cap.id)">{{ ruleLabel('action', cap.id, locale) }}</button>
            </aside>
            <div ref="canvasElement" class="rule-flow" data-testid="rule-canvas" tabindex="0" @dblclick="canvasDoubleClick">
                <VueFlow :id="flowId" :nodes="nodes" :edges="edges" :apply-default="false" :default-viewport="modelValue.viewport" :min-zoom="0.15" :max-zoom="2.5" :zoom-on-double-click="false" :is-valid-connection="validConnection" :delete-key-code="['Backspace', 'Delete']" :fit-view-on-init="!modelValue.layoutRestored" @nodes-initialized="initializeLayout" @nodes-change="changes" @edges-change="edgeChanges" @connect="connect" @connect-start="startConnection" @connect-end="endConnection" @pane-context-menu="canvasContextMenu" @node-click="select($event.node.id)" @move-end="publish({...modelValue, viewport: $event.flowTransform})">
                    <template #node-rule-module="nodeProps">
                        <RuleCanvasNode v-bind="nodeProps" @update:data="emit('edit', nodeProps.id, $event)" @quick-add="openNodeQuickAdd(nodeProps.id, $event)" @edit-start="emit('edit-start', nodeProps.id)" @edit-end="emit('edit-end', nodeProps.id)" />
                    </template>
                </VueFlow>
            </div>
        </div>
        <p v-if="connectionError" role="alert" class="text-xs text-red-500">{{ connectionError }}</p>
        <RuleNodeSearch :open="!!quick" :position="quick?.screen ?? {x: 0, y: 0}" :schema="schema" :phase="phase" :kind="quick?.kind" :wrap="quick?.mode === 'wrap'" :context="quickContext" @select="chooseQuick" @close="closeSearch" />
    </div>
</template>
<style scoped>
.rule-canvas-shell { min-width: 0; border: 1px solid var(--color-line); border-radius: 12px; background: var(--color-surface); overflow: hidden; }
.rule-canvas-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 16px; padding: 10px 12px; border-bottom: 1px solid var(--color-line); }
.rule-toolbar-group { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; }
.rule-toolbar-group + .rule-toolbar-group { padding-left: 16px; border-left: 1px solid var(--color-line); }
.rule-canvas-toolbar .btn { height: 32px; padding: 5px 10px; border-radius: 6px; font-size: 11px; }
.rule-canvas-toolbar svg { width: 14px; height: 14px; flex-shrink: 0; }
.rule-canvas-hint { margin: 0; padding: 8px 12px; font-size: 11px; color: var(--color-ink-muted); }
.rule-canvas-body { display: flex; min-width: 0; position: relative; border-top: 1px solid var(--color-line); }
.rule-module-library { display: none; width: 220px; flex-shrink: 0; max-height: 760px; overflow-y: auto; padding: 12px; border-right: 1px solid var(--color-line); background: var(--color-surface-elevated); }
.rule-module-library.is-open { display: block; }
.rule-module-library h3 { font-size: 11px; font-weight: 650; color: var(--color-ink-muted); margin: 16px 0 6px; }
.module-item { width: 100%; text-align: left; font-size: 12px; padding: 8px; border-radius: 6px; }
.module-item:hover { background: var(--color-surface-2); color: var(--color-accent); }
.rule-flow { flex: 1; min-width: 0; height: 760px; background-color: var(--color-canvas); background-image: radial-gradient(var(--color-line-strong) 1px, transparent 1px); background-size: 24px 24px; }
.library-close { display: inline-flex; margin-bottom: 8px; }
:deep(.vue-flow__node.selected .rule-graph-node) { outline: 2px solid var(--color-accent); outline-offset: 2px; }
:deep(.vue-flow__handle) { background: #818cf8; width: 10px; height: 10px; border: 2px solid var(--color-surface-elevated); }
:deep(.vue-flow__handle[data-handleid^='action']) { background: #d29958; border-radius: 50%; }
:deep(.vue-flow__edge-text) { fill: var(--color-ink-muted); font-size: 10px; }
:deep(.vue-flow__edge-textbg) { fill: var(--color-surface-elevated); }
@media(max-width:1100px) { .rule-module-library { position: absolute; left: 0; top: 0; bottom: 0; width: min(260px, 80vw); z-index: 20; box-shadow: 5px 0 20px rgb(0 0 0 / 15%); } }
@media(max-width:640px) { .rule-flow { height: 620px; } .rule-canvas-toolbar { gap: 6px; padding: 8px; } .rule-toolbar-group + .rule-toolbar-group { padding-left: 0; border-left: 0; } .rule-canvas-toolbar .btn { padding: 5px 7px; } }
</style>
