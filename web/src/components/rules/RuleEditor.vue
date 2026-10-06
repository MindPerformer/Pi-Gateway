<script setup lang="ts">
import {computed, ref, shallowRef, watch, onBeforeUnmount} from 'vue'
import {api, ApiError} from '../../api/client'
import type {Rule, RuleAction, RuleCondition, RuleFieldError, RuleSchema} from '../../api/rules'
import type {RuleCanvasDiagnostics, RuleDebugFocus} from '../../api/ruleSimulation'
import {useI18n} from '../../i18n'
import {useToastStore} from '../../stores/ui'
import {clone, parseRule, RuleInputError, stringifyRule, validateRule, type RuleDraft} from '../../utils/ruleEditor'
import {graphErrors, graphNodeValue, graphPathMap, graphSignature, graphToRule, persistGraphLayout, replaceGraphNode, ruleToGraph, type RuleGraph} from '../../utils/ruleGraph'
import {readonlyRuleFields} from '../../utils/ruleSchemaAdapter'
import {ruleLabel, ruleDisplayName} from '../../utils/ruleLabels'
import RuleField from './RuleField.vue'
import ConditionEditor from './ConditionEditor.vue'
import ActionEditor from './ActionEditor.vue'
import RuleHelp from './RuleHelp.vue'
import RuleCanvas from './RuleCanvas.vue'
import CaptureRuleDebugger from './CaptureRuleDebugger.vue'
const props = defineProps<{modelValue: RuleDraft; schema: RuleSchema; busy?: boolean; conflict?: Rule | null}>()
const emit = defineEmits<{'update:modelValue': [value: RuleDraft]; save: [rule: Rule]; close: []; reload: []; rebase: []}>()
const {t, locale} = useI18n()
const toast = useToastStore()
const form = ref<HTMLFormElement>()
const canvas = ref<InstanceType<typeof RuleCanvas>>()
const rule = computed(() => props.modelValue.rule)
const graph = computed(() => props.modelValue.graph ?? ruleToGraph(rule.value))
const paths = computed(() => graphPathMap(graph.value))
const graphProblems = computed(() => graphErrors(graph.value, props.schema))
const debuggerDisabled = computed(() => !!props.busy || graphProblems.value.length > 0 || props.modelValue.mode === 'code')
const sample = shallowRef<unknown>()
let editingNode: string | undefined
let inlineHistoryRecorded = false
watch(debuggerDisabled, disabled=>{if(disabled) sample.value=undefined}, {flush:'sync'})
onBeforeUnmount(()=>persistGraphLayout(graph.value))
function receiveSample(value:unknown) { sample.value=debuggerDisabled.value ? undefined : value }
function receiveDiagnostics(value:RuleCanvasDiagnostics|null) { diagnostics.value=value; if(!value)sample.value=undefined }
const selected = computed(() => graph.value.nodes.find(n => graph.value.selected.includes(n.id)) ?? graph.value.nodes.find(n => n.kind === 'rule'))
const selectedValue = computed(() => selected.value ? graphNodeValue(graph.value,selected.value.id) : undefined)
const selectedPath = computed(() => selected.value ? paths.value[selected.value.id] ?? selected.value.path : '')
const panelOpen = ref(false)
const validating = ref(false)
const diagnostics = shallowRef<RuleCanvasDiagnostics|null>(null)
const diagnosticLabels = computed(() => {
    const result: Record<string,string> = {}
    if (!diagnostics.value) return result
    const current = diagnostics.value
    for (const condition of current.conditions ?? []) if (condition.rule_id === current.ruleId) result[condition.path] = ruleLabel('status',condition.status,locale.value)
    for (const trace of current.traces ?? []) {
        if (trace.rule_id !== current.ruleId) continue
        const action = graph.value.nodes.find(n => n.kind === 'action' && (n.data as RuleAction).id === trace.action_id)
        const path = action ? paths.value[action.id] : undefined
        if (path) result[path] = ruleLabel('status',trace.rolled_back ? 'rolled_back' : trace.error ? 'failed' : trace.status ?? (trace.matched === false ? 'not_matched' : 'success'),locale.value)
    }
    return result
})
const normalFields = computed(() => props.schema.rule_fields.filter(f => !['when','actions','name','enabled','phase'].includes(f.name) && !readonlyRuleFields.includes(f.name)))
const topFields = computed(() => props.schema.rule_fields.filter(f => ['name','enabled','phase'].includes(f.name)))
function patch(fields:Partial<RuleDraft>) {emit('update:modelValue',{...props.modelValue,...fields})}
function updateGraph(next:RuleGraph, history=true) {
    const changed = graphSignature(next) !== graphSignature(graph.value)
    persistGraphLayout(next)
    if (!changed) {patch({graph:next}); return}
    const fields:Partial<RuleDraft>={graph:next,errors:[]}
    if (history) {fields.graphUndo=[...(props.modelValue.graphUndo ?? []),clone(graph.value)].slice(-50); fields.graphRedo=[]}
    try {fields.rule=graphToRule(next,props.schema,false); fields.code=stringifyRule(fields.rule)} catch { /* retain the last representable rule alongside this invalid graph */ }
    diagnostics.value=null; sample.value=undefined; patch(fields)
}
function updateRule(next:Rule) {updateGraph(ruleToGraph(next,graph.value))}
function updateField(name:string,value:unknown) {
    const root = graph.value.nodes.find(n=>n.kind==='rule')!
    const data = {...root.data} as unknown as Record<string,unknown>
    if(value===undefined) delete data[name]; else data[name]=value
    updateGraph(replaceGraphNode(graph.value,root.id,data as unknown as Rule))
}
function beginNodeEdit(id:string) {
    if(editingNode===id)return
    editingNode=id;inlineHistoryRecorded=false
    if(!graph.value.selected.includes(id))updateGraph({...graph.value,selected:[id]},false)
}
function endNodeEdit(id?:string) {
    if(id!==undefined&&editingNode!==id)return
    editingNode=undefined;inlineHistoryRecorded=false
}
function updateNodeById(id:string,data:Rule|RuleCondition|RuleAction) {
    if(!graph.value.nodes.some(node=>node.id===id))return
    const next=replaceGraphNode(graph.value,id,data)
    const changed=graphSignature(next)!==graphSignature(graph.value)
    updateGraph(next,editingNode!==id||!inlineHistoryRecorded)
    if(changed&&editingNode===id)inlineHistoryRecorded=true
}
function openAdvanced(id:string) {
    endNodeEdit()
    if(!graph.value.selected.includes(id))updateGraph({...graph.value,selected:[id]},false)
    panelOpen.value=true
}
function updateNode(data:RuleCondition|RuleAction) {if(selected.value) updateGraph(replaceGraphNode(graph.value,selected.value.id,data))}
function updateAction(actions:RuleAction[]) {
    if(!selected.value) return
    if(actions[0]) updateNode(actions[0])
    else updateGraph({...graph.value,nodes:graph.value.nodes.filter(n=>n.id!==selected.value!.id),edges:graph.value.edges.filter(e=>e.source!==selected.value!.id&&e.target!==selected.value!.id)})
}
function undo(redo=false) {
    endNodeEdit()
    const stack=redo?props.modelValue.graphRedo:props.modelValue.graphUndo
    const next=stack?.at(-1); if(!next) return
    const other=redo?props.modelValue.graphUndo:props.modelValue.graphRedo
    const fields:Partial<RuleDraft>={graph:clone(next),errors:[],graphUndo:redo?[...(other??[]),clone(graph.value)].slice(-50):stack!.slice(0,-1),graphRedo:redo?stack!.slice(0,-1):[...(other??[]),clone(graph.value)].slice(-50)}
    try {fields.rule=graphToRule(next,props.schema,false);fields.code=stringifyRule(fields.rule)} catch {}
    diagnostics.value=null; sample.value=undefined;patch(fields)
}
function reportErrors(error:unknown) {
    let errors:RuleFieldError[]=[]
    if(error instanceof RuleInputError) errors=error.errors
    else if(error instanceof ApiError && error.payload && typeof error.payload==='object') {
        const payload=error.payload as {errors?:RuleFieldError[]}
        if(Array.isArray(payload.errors)) errors=payload.errors
    }
    if(!errors.length) errors=[{path:'',message:error instanceof Error?error.message:String(error)}]
    patch({errors})
}
function synchronize():Rule|undefined {
    try {
        if(form.value && !form.value.reportValidity()) return
        const next=props.modelValue.mode==='code'?parseRule(props.modelValue.code,props.schema):graphToRule(graph.value,props.schema)
        const errors=validateRule(next,props.schema)
        if(errors.length) {patch({errors});return}
        patch({rule:next,errors:[]});return next
    } catch(error) {reportErrors(error)}
}
function switchMode(mode:'visual'|'code') {
    if(mode===props.modelValue.mode) return
    const next=synchronize();if(!next) return
    patch({rule:next,code:stringifyRule(next),graph:mode==='visual'?ruleToGraph(next,graph.value):graph.value,mode,errors:[]})
}
function formatCode() {
    try {const next=parseRule(props.modelValue.code,props.schema);patch({rule:next,code:stringifyRule(next),graph:ruleToGraph(next,graph.value),errors:[]})}catch(error){reportErrors(error)}
}
async function validate() {
    const next=synchronize();if(!next)return
    validating.value=true
    try{const result=await api.validateRule(next);patch({errors:result.errors??[]});if(result.valid)toast.success(t('rules.validate'))}catch(error){reportErrors(error)}finally{validating.value=false}
}
function save(){const next=synchronize();if(next)emit('save',next)}
function loadExample(example:Rule) {
    if(!confirm(t('rules.exampleConfirm')))return
    const next=clone(example)
    for(const key of readonlyRuleFields){const current=(rule.value as unknown as Record<string,unknown>)[key];if(current===undefined)delete(next as unknown as Record<string,unknown>)[key];else(next as unknown as Record<string,unknown>)[key]=current}
    updateRule(next)
}
async function copyCode(){try{const next=synchronize();if(next)await navigator.clipboard.writeText(props.modelValue.mode==='code'?props.modelValue.code:stringifyRule(next))}catch{toast.error(t('common.clipboardBlocked'))}}
function focusPath(path:string){const entry=Object.entries(paths.value).filter(([,value])=>path===value||path.startsWith(`${value}/`)).sort((a,b)=>b[1].length-a[1].length)[0];if(entry){canvas.value?.focus(entry[0]);panelOpen.value=true}}
function debugFocus(focus:RuleDebugFocus){const action=graph.value.nodes.find(n=>n.kind==='action'&&(n.data as RuleAction).id===focus.actionId);if(action){canvas.value?.focus(action.id);panelOpen.value=true}else if(focus.path)focusPath(focus.path)}
const shownErrors=computed(()=>[...props.modelValue.errors,...(props.modelValue.mode==='visual'?graphProblems.value.filter(error=>error.code?.startsWith('graph.')):[])].filter((error,index,all)=>all.findIndex(e=>e.path===error.path&&e.message===error.message)===index))
</script>
<template>
  <form ref="form" class="space-y-4 rule-editor" @submit.prevent="save">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-base font-semibold">{{ ruleDisplayName(rule,locale) || t('rules.add') }}</h2>
      <div class="flex flex-wrap gap-2"><button type="button" class="btn" @click="emit('close')">{{ t('rules.cancel') }}</button><button class="btn btn-primary" :disabled="busy" type="submit">{{ t('rules.save') }}</button></div>
    </div>
    <div v-if="conflict" class="notice space-y-2" role="alert"><p>{{ t('rules.conflict') }}</p><details><summary>{{ t('rules.serverVersion') }}</summary><pre class="max-h-72 overflow-auto whitespace-pre-wrap text-xs">{{ JSON.stringify(conflict,null,2) }}</pre></details><div class="flex flex-wrap gap-2"><button type="button" class="btn" @click="emit('reload')">{{ t('rules.useServer') }}</button><button type="button" class="btn" @click="emit('rebase')">{{ t('rules.rebase') }}</button></div></div>
    <div class="flex flex-wrap gap-2"><button type="button" class="btn" :aria-pressed="modelValue.mode==='visual'" @click="switchMode('visual')">{{ t('rules.graphical') }}</button><button type="button" class="btn" :aria-pressed="modelValue.mode==='code'" @click="switchMode('code')">{{ t('rules.code') }}</button><button type="button" class="btn" :disabled="validating||busy" @click="validate">{{ t('rules.validate') }}</button></div>
    <div v-if="shownErrors.length" class="rounded border border-red-500 p-3 text-xs text-red-500" role="alert"><h3 class="font-medium">{{ t('rules.errors') }}</h3><div v-for="(error,index) in shownErrors" :key="index" class="mt-1 break-words"><button type="button" class="text-left underline" @click="focusPath(error.path)">{{ error.code?.startsWith('graph.') ? t(`rules.${error.code}`) : t('rules.canvas.error') }}</button><details><summary>{{ t('rules.canvas.details') }}</summary><code>{{ error.path||'/' }}</code>: {{ error.message }}</details></div></div>
    <template v-if="modelValue.mode==='visual'">
      <div class="grid items-start gap-3 sm:grid-cols-[minmax(12rem,1fr)_minmax(8rem,.6fr)_auto]"><RuleField v-for="field in topFields" :key="field.name" :field="field" :model-value="(rule as unknown as Record<string,unknown>)[field.name]" :schema="schema" :path="`/${field.name}`" :errors="modelValue.errors" @update:model-value="updateField(field.name,$event)" /></div>
      <div class="canvas-editor-layout" :class="{'has-parameters':panelOpen}">
        <RuleCanvas ref="canvas" :model-value="graph" :schema="schema" :diagnostics="diagnosticLabels" :errors="[...modelValue.errors,...graphProblems]" :sample="sample" :can-undo="!!modelValue.graphUndo?.length" :can-redo="!!modelValue.graphRedo?.length" @update:model-value="updateGraph" @edit="updateNodeById" @advanced="openAdvanced" @edit-start="beginNodeEdit" @edit-end="endNodeEdit" @undo="undo()" @redo="undo(true)" />
        <aside v-if="panelOpen" class="node-parameter-panel is-open" data-testid="node-parameters">
          <div class="flex items-center justify-between gap-2"><h3 class="text-sm font-semibold">{{ t('rules.canvas.parameters') }}</h3><button type="button" class="btn panel-close" @click="panelOpen=false">{{ t('common.close') }}</button></div>
          <template v-if="selected?.kind==='condition'"><ConditionEditor :model-value="selectedValue as RuleCondition" :schema="schema" :path="selectedPath" :errors="modelValue.errors" :sample="sample" compact @update:model-value="updateNode" /></template>
          <template v-else-if="selected?.kind==='action'"><ActionEditor :model-value="[selectedValue as RuleAction]" :schema="schema" :phase="rule.phase" :errors="modelValue.errors" :sample="sample" :path-offset="Number(selectedPath.split('/')[2]) || 0" compact @update:model-value="updateAction" /></template>
          <template v-else><h4 class="text-xs font-medium">{{ t('rules.canvas.settings') }}</h4><RuleField v-for="field in normalFields" :key="field.name" :field="field" :model-value="(rule as unknown as Record<string,unknown>)[field.name]" :schema="schema" :path="`/${field.name}`" :errors="modelValue.errors" @update:model-value="updateField(field.name,$event)" /></template>
          <details v-if="selected" class="text-xs text-[color:var(--color-ink-muted)]"><summary>{{ t('rules.canvas.details') }}</summary><code>{{ selectedPath||'/' }}</code><dl v-if="selected.kind==='rule'"><div v-for="key in readonlyRuleFields" :key="key"><dt>{{ ruleLabel('field',key,locale) }}</dt><dd>{{ (rule as unknown as Record<string,unknown>)[key] }}</dd></div></dl></details>
        </aside>
      </div>
      <button type="button" class="btn show-parameters" @click="panelOpen=true">{{ t('rules.canvas.parameters') }}</button>
    </template>
    <div v-else class="space-y-2"><div class="flex gap-2"><button type="button" class="btn" @click="formatCode">{{ t('rules.format') }}</button><button type="button" class="btn" @click="copyCode">{{ t('rules.copyCode') }}</button></div><textarea data-testid="rule-code" class="input min-h-[28rem] font-mono text-xs" spellcheck="false" :value="modelValue.code" :aria-label="t('rules.code')" @input="patch({code:($event.target as HTMLTextAreaElement).value})" /></div>
    <CaptureRuleDebugger :rule="rule" :schema="schema" :disabled="debuggerDisabled" @diagnostics="receiveDiagnostics" @focus="debugFocus" @errors="patch({errors:$event})" @sample="receiveSample" />
    <RuleHelp :schema="schema" @example="loadExample" />
  </form>
</template>
<style scoped>
.canvas-editor-layout { display: grid; grid-template-columns: minmax(0,1fr); gap: .75rem; position: relative; }
.canvas-editor-layout.has-parameters { grid-template-columns: minmax(0,1fr) 20rem; }
.node-parameter-panel { display: flex; flex-direction: column; gap: .8rem; min-width: 0; max-height: 660px; overflow-y: auto; border: 1px solid var(--color-line); border-radius: .6rem; padding: .75rem; background: var(--color-surface-elevated, var(--color-surface, var(--color-canvas))); }
.panel-close { display: inline-flex; }
.show-parameters { display: none; }
@media(max-width:1300px) { .canvas-editor-layout.has-parameters { grid-template-columns: minmax(0,1fr); } .node-parameter-panel { display: none; } .node-parameter-panel.is-open { display: flex; position: fixed; z-index: 50; top: 4rem; bottom: 1rem; right: 1rem; width: min(90vw,25rem); max-height: none; box-shadow: 0 8px 35px rgb(0 0 0 / 20%); } .panel-close, .show-parameters { display: inline-flex; } }
</style>
