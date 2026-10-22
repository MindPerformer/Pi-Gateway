<script setup lang="ts">
import {computed, nextTick, onBeforeUnmount, onMounted, ref, watch} from 'vue'
import type {RulePhase, RuleSchema} from '../../api/rules'
import {useI18n} from '../../i18n'
import {editorId} from '../../utils/editorId'
import {ruleLabel, ruleOptionLabel} from '../../utils/ruleLabels'
import {localHelp} from '../../utils/ruleSchema'

const props = defineProps<{
    open: boolean
    position: {x: number; y: number}
    schema: RuleSchema
    phase: RulePhase
    kind?: 'condition' | 'action' | 'value'
    wrap?: boolean
    context: string
}>()
const emit = defineEmits<{
    select: [value: {kind: 'condition' | 'action' | 'value'; type: string}]
    combination: [value: 'tool' | 'tools' | 'field' | 'model']
    close: []
}>()
const {t, locale} = useI18n()
const popup = ref<HTMLElement>()
const input = ref<HTMLInputElement>()
const query = ref('')
const filter = ref<'' | 'condition' | 'action'>('')
const active = ref(0)
const listID = `node-search-${editorId()}`
const viewport = ref({width: 1024, height: 768})
const entries = computed(() => {
    const text = query.value.trim().toLocaleLowerCase()
    if (props.kind==='value') {
        const computed = props.schema.value_expressions?.find(cap=>cap.id==='computed')?.fields.find(field=>field.name==='op')
        return ['literal','reference',...(computed?.enum??[])].map(type=>({kind:'value' as const,type,label:['literal','reference'].includes(type)?t(`rules.canvas.value.${type}`):ruleOptionLabel(type,locale.value),help:computed?.enum_help?.[type]??t('rules.canvas.valueHelp')})).filter(entry=>!text||[entry.type,entry.label,entry.help].some(value=>value.toLowerCase().includes(text)))
    }
    const modules = ([['condition', props.schema.conditions], ['action', props.schema.actions]] as const).flatMap(([kind, capabilities]) => {
        if ((props.kind && props.kind !== kind) || (filter.value && filter.value !== kind)) { return [] }
        return capabilities.filter(capability => {
            if (kind === 'action' && capability.id === 'sequence') { return false }
            if (props.wrap && (kind !== 'condition' || !['all', 'any', 'not'].includes(capability.id))) { return false }
            if (kind === 'action' && capability.phases?.length && !capability.phases.includes(props.phase)) { return false }
            return !text || [capability.id, ruleLabel(kind, capability.id, 'zh-CN'), ruleLabel(kind, capability.id, 'en'), localHelp(capability.description, 'zh-CN'), localHelp(capability.description, 'en')].some(value => value.toLocaleLowerCase().includes(text))
        }).map(capability => ({kind, type: capability.id, label: ruleLabel(kind, capability.id, locale.value), help: localHelp(capability.description, locale.value)}))
    })
    const combinations = (() => {
    if (!props.wrap && props.kind !== 'condition' && filter.value !== 'condition') {
        return ([['tool', 'removeTool'], ['tools', 'removeAllTools'], ['field', 'removeField'], ['model', 'setModel']] as const).filter(([, label]) => !text || ['zh-CN', 'en'].some(lang => (lang === 'zh-CN' ? {removeTool:'按名称移除工具',removeAllTools:'移除全部工具',removeField:'移除字段',setModel:'设置模型'} : {removeTool:'Remove tool by name',removeAllTools:'Remove all tools',removeField:'Remove a field',setModel:'Set model'})[label].toLowerCase().includes(text))).map(([type, label]) => ({kind: 'action' as const, type, combination: type, label: t(`rules.canvas.${label}`), help: t('rules.canvas.combinationHint')}))
    }
    return []
})()
    return [...combinations, ...modules]
})
const positionStyle = computed(() => {
    const width = Math.min(360, viewport.value.width - 24)
    const height = Math.min(480, viewport.value.height - 24)
    return {left: `${Math.max(12, Math.min(props.position.x, viewport.value.width - width - 12))}px`, top: `${Math.max(12, Math.min(props.position.y, viewport.value.height - height - 12))}px`, width: `${width}px`}
})
function resize() { viewport.value = {width: window.innerWidth, height: window.innerHeight} }
function outside(event: PointerEvent) {
    if (props.open && event.target instanceof Node && !popup.value?.contains(event.target)) { emit('close') }
}
function choose(index: number) {
    const entry = entries.value[index]
    if (entry && 'combination' in entry) { emit('combination', entry.combination as 'tool' | 'tools' | 'field' | 'model') }
    else if (entry) { emit('select', {kind: entry.kind, type: entry.type}) }
}
function keydown(event: KeyboardEvent) {
    if (event.isComposing) { return }
    if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        emit('close')
        return
    }
    const target = event.target as HTMLElement
    const result = target.closest<HTMLButtonElement>('[data-search-index]')
    if (target !== input.value && !result) { return }
    if (event.key === 'Enter' && target === input.value) {
        event.preventDefault()
        event.stopPropagation()
        choose(active.value)
        return
    }
    const arrows = ['ArrowDown', 'ArrowUp'].includes(event.key)
    const boundary = !!result && ['Home', 'End'].includes(event.key)
    if ((!arrows && !boundary) || !entries.value.length) { return }
    event.preventDefault()
    event.stopPropagation()
    focusCommand(event.key, result)
}
function focusCommand(key: string, result: HTMLButtonElement | null) {
    if (!result) { active.value = key === 'ArrowUp' ? entries.value.length - 1 : 0 }
    else if (key === 'Home') { active.value = 0 }
    else if (key === 'End') { active.value = entries.value.length - 1 }
    else { active.value = (Number(result.dataset.searchIndex) + (key === 'ArrowDown' ? 1 : -1) + entries.value.length) % entries.value.length }
    popup.value?.querySelector<HTMLButtonElement>(`[data-search-index="${active.value}"]`)?.focus()
}
watch(entries, () => { active.value = 0 })
watch(() => props.open, async open => {
    if (!open) { return }
    query.value = ''
    filter.value = ''
    active.value = 0
    resize()
    await nextTick()
    input.value?.focus()
})
onMounted(() => { resize(); window.addEventListener('resize', resize); document.addEventListener('pointerdown', outside, true) })
onBeforeUnmount(() => { window.removeEventListener('resize', resize); document.removeEventListener('pointerdown', outside, true) })
</script>

<template>
    <Teleport to="body">
        <dialog open v-if="open" ref="popup" class="rule-node-search nodrag nopan nowheel" :style="positionStyle" data-testid="node-search" :aria-label="t('rules.canvas.addNode')" @keydown="keydown" @pointerdown.stop @wheel.stop @contextmenu.prevent>
            <header><strong>{{ t('rules.canvas.addNode') }}</strong><button type="button" class="btn btn-ghost" :aria-label="t('common.close')" @click="emit('close')">×</button></header>
            <p class="search-context" data-testid="node-search-context">{{ context }}</p>
            <input ref="input" v-model="query" class="input" data-testid="node-search-input" :placeholder="t('rules.canvas.search')" :aria-label="t('rules.canvas.search')" type="search" :aria-controls="listID" :aria-describedby="`${listID}-keys`" autocomplete="off" />
            <div v-if="!kind && !wrap" class="search-filters">
                <button v-for="item in (['', 'condition', 'action'] as const)" :key="item" type="button" :class="{active: filter === item}" :aria-pressed="filter === item" @click="filter = item; input?.focus()">{{ t((() => { if (item === '') { return 'rules.canvas.allModules'; } if (item === 'condition') { return 'rules.addCondition'; } return 'rules.addAction'; })()) }}</button>
            </div>
            <!-- These are immediate commands, not value selections: native buttons retain rich help and real keyboard focus. -->
            <!-- Visible native markers preserve list semantics, including Safari/VoiceOver, without a redundant role. -->
            <ul :id="listID" class="search-results" :aria-label="t('rules.canvas.searchResults')">
                <li v-for="(entry, index) in entries" :key="`${entry.kind}:${entry.type}`"><button :id="`${listID}-${index}`" :data-testid="`node-search-${entry.kind}-${entry.type}`" :data-search-index="index" class="search-entry" :class="{active: index === active}" type="button" :aria-labelledby="`${listID}-${index}-title`" :aria-describedby="`${listID}-${index}-help`" @focus="active = index" @click="choose(index)">
                    <span :id="`${listID}-${index}-title`" class="entry-title"><span class="entry-dot" :class="entry.kind" />{{ entry.label }}</span>
                    <small :id="`${listID}-${index}-help`">{{ entry.help }}</small>
                </button></li>
            </ul>
            <output class="search-empty" aria-live="polite" aria-atomic="true">{{ entries.length ? '' : t('rules.canvas.noSearchResults') }}</output>
            <footer :id="`${listID}-keys`">{{ t('rules.canvas.searchKeys') }}</footer>
        </dialog>
    </Teleport>
</template>

<style scoped>
.rule-node-search { margin: 0; position: fixed; z-index: 100; isolation: isolate; display: flex; flex-direction: column; gap: 9px; padding: 12px; max-height: min(480px, calc(100dvh - 24px)); overflow: hidden; border: 1px solid var(--color-line); border-radius: 10px; color: var(--color-ink); background: var(--color-surface-elevated, var(--color-surface, var(--color-canvas))); box-shadow: 0 12px 40px rgb(0 0 0 / 24%); }
.rule-node-search header { display: flex; justify-content: space-between; align-items: center; font-size: 14px; }
.search-context, .search-empty, footer { margin: 0; font-size: 11px; color: var(--color-ink-muted); line-height: 1.5; overflow-wrap: anywhere; }
.search-filters { display: flex; gap: 5px; }
.search-filters button { padding: 4px 8px; font-size: 11px; border-radius: 5px; }
.search-filters button.active, .search-entry.active, .search-entry:focus-visible { background: var(--color-accent-soft); color: var(--color-accent); }
.search-results { list-style: disc; margin: 0; padding: 0; padding-inline-start: 1.25rem; overflow-y: auto; min-height: 0; flex: 1; }
.search-results > li::marker { color: var(--color-ink-muted); }
.search-entry { display: grid; gap: 3px; width: 100%; padding: 9px; text-align: left; border-radius: 6px; }
.entry-title { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; }
.entry-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--color-accent); }
.entry-dot.action { border-radius: 2px; background: #d97706; }
.search-entry small { color: var(--color-ink-muted); font-size: 11px; line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
</style>
