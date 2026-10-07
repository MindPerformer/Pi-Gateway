<script setup lang="ts">
import {Handle,Position,type NodeProps} from '@vue-flow/core'
import type {RuleGraphGroup} from '../../utils/ruleGraph'
import {useI18n} from '../../i18n'
const props=defineProps<NodeProps<{group:RuleGraphGroup;ports:RuleGraphGroup['ports']; labels:Record<string,string>}>>()
const emit=defineEmits<{enter:[id:string];ungroup:[id:string];rename:[id:string,name:string];register:[id:string];port:[id:string,port:string,name:string]}>()
const {locale}=useI18n()
</script>
<template>
 <div class="rule-group-node" :data-group-id="id">
  <header class="rule-node-drag-handle"><span>{{locale==='zh-CN'?'组':'Group'}}</span><strong>{{data.group.name}}</strong><span>{{data.group.nodes.length}}</span></header>
  <div class="group-controls nodrag nopan nowheel" @pointerdown.stop @mousedown.stop @click.stop @keydown.stop>
   <input class="input" :aria-label="locale==='zh-CN'?'组名':'Group name'" :value="data.group.name" maxlength="100" @change="emit('rename',id,($event.target as HTMLInputElement).value)" />
   <div v-for="port in data.ports" :key="port.id" class="group-port" :class="port.direction">
    <Handle :id="`group:${port.id}`" :type="port.direction==='input'?'target':'source'" :position="port.direction==='input'?Position.Left:Position.Right" />
    <span>{{port.direction==='input'?'↳':'↗'}}</span><input class="input" :aria-label="locale==='zh-CN'?'端口名称':'Port name'" :value="port.name || data.labels[port.id]" @change="emit('port',id,port.id,($event.target as HTMLInputElement).value)" />
   </div>
   <div class="group-buttons"><button type="button" class="btn" :data-testid="`enter-group-${id}`" @click="emit('enter',id)">{{locale==='zh-CN'?'进入组':'Enter group'}}</button><button type="button" class="btn" @click="emit('register',id)">{{locale==='zh-CN'?'管理端口':'Manage ports'}}</button><button type="button" class="btn" @click="emit('ungroup',id)">{{locale==='zh-CN'?'解散':'Ungroup'}}</button></div>
  </div>
 </div>
</template>
<style scoped>
.rule-group-node {width:400px;border:2px solid #8b7ed5;border-radius:10px;background:var(--color-surface-elevated);color:var(--color-ink);box-shadow:0 4px 18px rgb(0 0 0 / 12%)}
header {display:flex;align-items:center;gap:10px;padding:12px 14px;background:color-mix(in srgb,#8b7ed5 15%,var(--color-surface-elevated));cursor:grab;font-size:11px}
header strong {flex:1;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.group-controls {padding:12px;display:grid;gap:8px}
.group-port {position:relative;display:grid;grid-template-columns:18px minmax(0,1fr);align-items:center;gap:6px}
.group-port :deep(.vue-flow__handle) {top:50%}
.group-port.input :deep(.vue-flow__handle) {left:-19px}
.group-port.output :deep(.vue-flow__handle) {right:-19px}
.input {min-width:0;width:100%;height:30px;font-size:11px}
.group-buttons {display:flex;gap:6px}
.btn {flex:1;font-size:11px;padding:6px}
</style>
