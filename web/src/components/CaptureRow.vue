<script setup lang="ts">
import type { Capture } from '../api/types'
import { RouterLink } from 'vue-router'
import Badge from './Badge.vue'
import { formatBytes, formatDuration, formatRelative } from '../stores/ui'
import { useI18n } from '../i18n'

defineProps<{
	capture: Capture
}>()

const { t } = useI18n()

const outcomeTone = (outcome: string) => {
	if (outcome === 'ok') return 'success'
	if (outcome === 'error') return 'danger'
	if (outcome === 'aborted') return 'warn'
	return 'neutral'
}

const statusTone = (status: number) => {
	if (status >= 200 && status < 300) return 'success'
	if (status >= 400) return 'danger'
	return 'neutral'
}
</script>

<template>
	<tr class="row-hover cursor-pointer">
		<td class="td font-mono text-[12px]"><RouterLink :to="`/captures/${capture.id}`" class="text-[color:var(--color-accent)] hover:underline" @click.stop>#{{ capture.id }}</RouterLink></td>
		<td class="td whitespace-nowrap text-[color:var(--color-ink-muted)]">{{ formatRelative(capture.created_at) }}</td>
		<td class="td">
			<div class="font-bold">{{ capture.account_name || '—' }}</div>
			<div class="text-[11px] text-[color:var(--color-ink-faint)]">{{ capture.api_key_name || '—' }}</div>
		</td>
		<td class="td font-mono text-[12px]">{{ capture.model || '—' }}</td>
		<td class="td">
			<div class="flex items-center gap-1">
				<Badge :tone="capture.client_transport === 'ws' ? 'info' : 'neutral'">{{ capture.client_transport || '?' }}</Badge>
				<span class="text-[color:var(--color-ink-faint)]">→</span>
				<Badge :tone="capture.upstream_transport === 'ws' ? 'info' : 'neutral'">{{ capture.upstream_transport || '?' }}</Badge>
			</div>
		</td>
		<td class="td">
			<Badge :tone="statusTone(capture.status)">{{ capture.status || '—' }}</Badge>
		</td>
		<td class="td">
			<Badge :tone="outcomeTone(capture.outcome)">{{ capture.outcome }}</Badge>
		</td>
		<td class="td text-right font-mono text-[12px] text-[color:var(--color-ink-muted)]">
			{{ formatDuration(capture.duration_ms) }}
		</td>
		<td class="td text-right font-mono text-[12px] text-[color:var(--color-ink-muted)]">
			{{ capture.total_tokens ? capture.total_tokens.toLocaleString() : '—' }}
		</td>
		<td class="td text-right font-mono text-[12px] text-[color:var(--color-ink-faint)]">
			{{ formatBytes(capture.request_bytes) }}
		</td>
	</tr>
</template>
