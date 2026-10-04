<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api/client'
import type { Middleware } from '../api/types'
import { useToastStore } from '../stores/ui'
import { useI18n } from '../i18n'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/Badge.vue'
import Toggle from '../components/Toggle.vue'
import { RefreshCw, RotateCcw } from 'lucide-vue-next'

const toast = useToastStore()
const { t } = useI18n()
const middlewares = ref<Middleware[]>([])
const loading = ref(false)
const drafts = ref<Record<string, string>>({})
const saving = ref<string>('')

async function load() {
	loading.value = true
	try {
		const result = await api.listMiddlewares()
		middlewares.value = result.middlewares ?? []
		drafts.value = Object.fromEntries(middlewares.value.map((m) => [m.name, m.config]))
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'failed to load middlewares')
	} finally {
		loading.value = false
	}
}

onMounted(load)

function dirty(middleware: Middleware) {
	return drafts.value[middleware.name] !== middleware.config
}

async function toggle(middleware: Middleware, enabled: boolean) {
	try {
		await api.updateMiddleware(middleware.name, { enabled })
		await load()
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'update failed')
	}
}

async function save(middleware: Middleware) {
	saving.value = middleware.name
	try {
		await api.updateMiddleware(middleware.name, { config: drafts.value[middleware.name], enabled: middleware.enabled })
		toast.success(middleware.name)
		await load()
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'invalid configuration')
	} finally {
		saving.value = ''
	}
}

function reset(middleware: Middleware) {
	drafts.value[middleware.name] = middleware.default_config
}

async function move(middleware: Middleware, delta: number) {
	try {
		await api.updateMiddleware(middleware.name, { order_index: middleware.order_index + delta, enabled: middleware.enabled })
		await load()
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'reorder failed')
	}
}
</script>

<template>
	<div class="page-view">
		<PageHeader :title="t('middlewares.title')" :subtitle="t('middlewares.subtitle')">
			<button class="btn" :disabled="loading" @click="load">
				<RefreshCw class="h-3.5 w-3.5" />
				{{ t('middlewares.reload') }}
			</button>
		</PageHeader>

		<div class="page-content space-y-5" :aria-busy="loading">
			<details class="notice">
				<summary class="font-medium">{{ t('middlewares.noteTitle') }}</summary>
				<p class="mt-2">{{ t('middlewares.note') }}</p>
			</details>
			<div v-if="loading && !middlewares.length" class="card empty-state" role="status">{{ t('stats.loading') }}</div>

			<div v-for="middleware in middlewares" :key="middleware.name" class="card panel-content">
				<div class="flex flex-wrap items-start justify-between gap-3">
					<div class="min-w-0">
						<div class="flex flex-wrap items-center gap-2">
							<Toggle :model-value="middleware.enabled" @update:modelValue="(v: boolean) => toggle(middleware, v)" />
							<code class="font-mono text-[13px] font-semibold">{{ middleware.name }}</code>
							<Badge :tone="middleware.enabled ? 'success' : 'neutral'">
								{{ middleware.enabled ? t('middlewares.enabled') : t('middlewares.disabled') }}
							</Badge>
							<Badge tone="neutral">{{ t('middlewares.order', { order: middleware.order_index }) }}</Badge>
						</div>
						<p class="mt-1.5 text-[12px] text-[color:var(--color-ink-muted)]">{{ middleware.description }}</p>
					</div>
					<div class="flex items-center gap-1">
						<button class="btn btn-ghost" @click="move(middleware, -10)">{{ t('middlewares.earlier') }}</button>
						<button class="btn btn-ghost" @click="move(middleware, 10)">{{ t('middlewares.later') }}</button>
					</div>
				</div>

				<div class="mt-3">
					<div class="mb-1 flex items-center justify-between">
						<span class="text-[11px] tracking-wide text-[color:var(--color-ink-faint)] uppercase">{{ t('middlewares.config') }}</span>
						<div class="flex items-center gap-1">
							<button class="btn btn-ghost !px-1.5 !py-0.5" :title="t('middlewares.reset')" @click="reset(middleware)">
								<RotateCcw class="h-3.5 w-3.5" />
							</button>
							<button class="btn !py-0.5" :disabled="!dirty(middleware) || saving === middleware.name" @click="save(middleware)">
								{{ saving === middleware.name ? t('middlewares.saving') : t('middlewares.save') }}
							</button>
						</div>
					</div>
					<textarea v-model="drafts[middleware.name]" class="input font-mono" rows="3" spellcheck="false" />
				</div>
			</div>
		</div>
	</div>
</template>
