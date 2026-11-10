<script setup lang="ts">
import { rateFields, type DecimalRates } from '../../api/pricing'
import { useI18n } from '../../i18n'
defineProps<{ rates: DecimalRates | null | undefined }>()
const { t } = useI18n()
</script>
<template><dl class="rate-summary"><div v-for="field in rateFields" :key="field"><dt>{{ t(`pricing.${field}`) }}</dt><dd>{{ rates?.[field] === '' || rates?.[field] == null ? '—' : `$${rates[field]}` }}</dd></div></dl></template>
<style scoped>.rate-summary{display:flex;flex-wrap:wrap;gap:6px 16px;margin:0;font-size:11px}.rate-summary>div{display:flex;gap:5px}.rate-summary dt{color:var(--color-ink-muted)}.rate-summary dd{margin:0;font-family:var(--font-mono)}</style>
