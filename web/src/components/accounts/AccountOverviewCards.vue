<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, Gauge, ShieldCheck, Users } from 'lucide-vue-next'
import type { Account } from '../../api/types'
import { useI18n } from '../../i18n'
import { metric } from '../../api/stats-format'
import { accountSummary } from './accountPresentation'

const props = defineProps<{ accounts: Account[]; loaded: boolean }>()
const { t } = useI18n()
const items = computed(() => {
	const summary = accountSummary(props.accounts)
	return [
		{ key: 'total', value: summary.total, caption: 'pool', icon: Users, tone: 'info' },
		{ key: 'normal', value: summary.normal, caption: 'ready', icon: ShieldCheck, tone: 'success' },
		{ key: 'limited', value: summary.limited, caption: 'limitHint', icon: Gauge, tone: 'warn' },
		{ key: 'pending', value: summary.disabled + summary.error, caption: 'pendingHint', icon: AlertTriangle, tone: 'danger' },
	]
})
</script>

<template>
	<section class="account-overview" :aria-label="t('dashboard.accountStatus')">
		<article v-for="item in items" :key="item.key" class="card overview-card">
			<div class="overview-copy">
				<p>{{ t(`accounts.overview.${item.key}`) }}</p>
				<strong>{{ loaded ? metric(item.value) : '—' }}</strong>
				<span>{{ t(`accounts.overview.${item.caption}`) }}</span>
			</div>
			<span class="overview-icon" :class="`tone-${item.tone}`"><component :is="item.icon" :size="18" /></span>
		</article>
	</section>
</template>

<style scoped>
.account-overview { display: grid; min-width: 0; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.overview-card { display: flex; height: 98px; min-width: 0; overflow: hidden; justify-content: space-between; gap: 12px; padding: 16px; }
.overview-copy { min-width: 0; overflow: hidden; display: flex; flex-direction: column; }
.overview-copy p { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-ink-muted); font-size: 12px; font-weight: 650; line-height: 1; }
.overview-copy strong { margin: 8px 0; font-family: var(--font-mono); font-size: 26px; font-weight: 800; line-height: 1; font-variant-numeric: tabular-nums; }
.overview-copy span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-ink-faint); font-size: 12px; font-weight: 500; line-height: 1; }
.overview-icon { width: 36px; height: 36px; flex-shrink: 0; display: inline-flex; align-items: center; justify-content: center; border-radius: 8px; }
.tone-info { color: var(--color-info); background: var(--color-info-soft); }
.tone-success { color: var(--color-success); background: var(--color-success-soft); }
.tone-warn { color: var(--color-warn); background: var(--color-warn-soft); }
.tone-danger { color: var(--color-danger); background: var(--color-danger-soft); }
@media (max-width: 1279px) { .account-overview { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 767px) { .account-overview { grid-template-columns: 1fr; } }
</style>
