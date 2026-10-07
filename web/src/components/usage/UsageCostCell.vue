<script setup lang="ts">
import { ref, useId, onBeforeUnmount } from 'vue'
import { Info } from 'lucide-vue-next'
import type { UsageRecord } from '../../api/types'
import { money } from '../../api/stats-format'
import { useI18n } from '../../i18n'
import UsageBillingDetails from './UsageBillingDetails.vue'
defineProps<{ record: UsageRecord }>()
const { t } = useI18n()
const open = ref(false)
const id = useId()
const position = ref({ top: '0px', left: '0px', maxHeight: '70vh' })
let hideTimer: ReturnType<typeof setTimeout> | undefined
function keepOpen() { clearTimeout(hideTimer); open.value = true }
function hide() { hideTimer = setTimeout(() => { open.value = false }, 120) }
function close() { clearTimeout(hideTimer); open.value = false }
onBeforeUnmount(() => clearTimeout(hideTimer))
function show(event: Event) {
	const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
	const width = Math.min(440, window.innerWidth - 24)
	const roomBelow = window.innerHeight - rect.bottom - 12
	const roomAbove = rect.top - 12
	const below = roomBelow >= 350 || roomBelow >= roomAbove
	position.value = {
		top: `${below ? rect.bottom + 6 : Math.max(12, rect.top - Math.min(480, roomAbove))}px`,
		left: `${Math.max(12, Math.min(rect.right - width, window.innerWidth - width - 12))}px`,
		maxHeight: `${Math.max(80, below ? roomBelow - 6 : Math.min(480, roomAbove))}px`,
	}
	keepOpen()
}
</script>

<template>
	<span class="usage-cost-cell">
		<span class="cost-value">{{ money(record.cost_usd) }}</span>
		<button type="button" class="cost-info" :aria-label="t('usage.billingDetails')" :aria-describedby="open ? id : undefined" @mouseenter="show" @mouseleave="hide" @focus="show" @blur="hide" @keydown.esc="close" @click="show"><Info :size="13" /></button>
		<Teleport to="body"><div v-if="open" :id="id" class="billing-tooltip" role="tooltip" :style="position" @mouseenter="keepOpen" @mouseleave="hide"><UsageBillingDetails :record="record" /></div></Teleport>
	</span>
</template>

<style scoped>
.usage-cost-cell { display: inline-flex; align-items: center; justify-content: flex-end; gap: 6px; }
.cost-value { color: var(--color-success); font-family: var(--font-mono); font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; }
.cost-info { display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 26px; padding: 0; border-radius: 5px; color: var(--color-ink-faint); }
.cost-info:hover, .cost-info:focus-visible { background: var(--color-surface-2); color: var(--color-info); }
.billing-tooltip { position: fixed; z-index: 1000; width: min(440px, calc(100vw - 24px)); overflow: auto; padding: 14px; border: 1px solid var(--color-line); border-radius: var(--radius-control); background: var(--color-surface); box-shadow: 0 8px 24px #0003; }
</style>
