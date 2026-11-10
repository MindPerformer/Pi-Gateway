<script setup lang="ts">
import { ref } from 'vue'
import { pricing, type HistoryEntry } from '../../api/pricing'
import { ApiError } from '../../api/client'
import Modal from '../Modal.vue'
import { formatDateTime, useI18n } from '../../i18n'
defineProps<{items: HistoryEntry[]; revision: number; disabled: boolean}>()
const emit = defineEmits<{changed: []}>()
const {t} = useI18n()
const target = ref<HistoryEntry | null>(null)
const busy = ref(false)
const error = ref('')
async function restore(revision: number) {if (!target.value || busy.value) return; busy.value = true; error.value = ''; try {await pricing.restore(revision, target.value.id); target.value = null; emit('changed')} catch(e) {error.value = e instanceof ApiError && e.status === 409 ? t('pricing.conflict') : e instanceof Error ? e.message : t('pricing.error')} finally {busy.value = false}}
</script>
<template><section class="pricing-form"><p v-if="!items.length">{{ t('pricing.noHistory') }}</p><article v-for="entry in items" :key="entry.id" class="pricing-card"><div class="pricing-toolbar"><strong>{{ entry.model || entry.action }}</strong><span>{{ formatDateTime(entry.created_at) }}</span><span>{{ t('pricing.revision') }} {{ entry.revision }}</span></div><p>{{ t('pricing.actor') }}: {{ entry.actor }} · {{ t('pricing.action') }}: {{ entry.action }}</p><details><summary>{{ t('pricing.snapshot') }}</summary><div class="pricing-grid"><div><h4>{{ t('pricing.before') }}</h4><pre>{{ JSON.stringify(entry.before, null, 2) }}</pre></div><div><h4>{{ t('pricing.after') }}</h4><pre>{{ JSON.stringify(entry.after, null, 2) }}</pre></div></div></details><button v-if="entry.model" class="btn btn-sm" :disabled="disabled || busy" @click="target = entry; error = ''">{{ t('pricing.restore') }}</button></article></section><Modal :open="!!target" :title="t('pricing.restore')" @close="!busy && (target = null)"><p>{{ t('pricing.restoreHint') }} {{ target?.model }}</p><div v-if="error"><p role="alert" class="pricing-error">{{ error }}</p><button class="btn btn-sm" :disabled="busy || disabled" @click="emit('changed')">{{ t('pricing.refresh') }}</button></div><template #footer><button class="btn" :disabled="busy" @click="target = null">{{ t('pricing.cancel') }}</button><button class="btn btn-primary" :disabled="busy" @click="restore(revision)">{{ t('pricing.confirm') }}</button></template></Modal></template>
