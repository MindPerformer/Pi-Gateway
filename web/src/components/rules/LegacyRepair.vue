<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../../api/client'
import type { Middleware } from '../../api/types'
import { safeParseJSON } from '../../utils/ruleEditor'
import { useI18n } from '../../i18n'
const emit = defineEmits<{ retry: []; dirty: [dirty: boolean] }>()
const { t } = useI18n()
const items = ref<Middleware[]>([])
const drafts = ref<Record<string,string>>({})
const error = ref('')
const busy = ref('')
async function load() {
  try {
    const result = await api.listMiddlewares()
    items.value = result.middlewares ?? []
    for (const item of items.value) if (!Object.hasOwn(drafts.value,item.name)) drafts.value[item.name] = item.config
  } catch (reason) { error.value = reason instanceof Error ? reason.message : String(reason) }
}
function changed() { emit('dirty',items.value.some(item => drafts.value[item.name] !== item.config)) }
async function save(item: Middleware) {
  busy.value = item.name; error.value = ''
  try {
    const config = drafts.value[item.name] ?? ''
    safeParseJSON(config)
    await api.updateMiddleware(item.name,{config,expected_revision:item.revision})
    item.config = config
    await load(); changed()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { busy.value = '' }
}
onMounted(load)
</script>
<template>
  <section class="card panel-content space-y-3">
    <h2 class="text-sm font-semibold">{{ t('rules.legacyRepair') }}</h2>
    <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.legacyNote') }}</p>
    <p v-if="error" class="text-xs text-red-500" role="alert">{{ error }}</p>
    <details v-for="item in items" :key="item.name" class="rounded border border-[color:var(--color-line)] p-3">
      <summary class="cursor-pointer font-mono text-xs">{{ item.name }}</summary>
      <p class="my-2 text-xs">{{ item.description }}</p>
      <p v-if="item.compatible === false" class="text-xs text-red-500">{{ t('rules.legacyIncompatible') }}</p>
      <textarea v-model="drafts[item.name]" :disabled="item.compatible === false" class="input mt-2 font-mono text-xs" rows="6" spellcheck="false" :aria-label="item.name" @input="changed" />
      <button class="btn mt-2" :disabled="!!busy || item.compatible === false || drafts[item.name] === item.config" @click="save(item)">{{ t('common.save') }}</button>
    </details>
    <button class="btn" :disabled="!!busy" @click="emit('retry')">{{ t('rules.retry') }}</button>
  </section>
</template>
