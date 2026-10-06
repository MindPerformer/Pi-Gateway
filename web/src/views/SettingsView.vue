<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api/client'
import type { Settings, SettingsResponse } from '../api/types'
import { strategyDescription, strategyLabel, transportDescription, transportLabel } from '../utils/uiOptions'
import { useToastStore, formatBytes } from '../stores/ui'
import { useI18n } from '../i18n'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/Badge.vue'
import Toggle from '../components/Toggle.vue'
import { Activity, Database, Fingerprint, Network, RefreshCw, Save, Settings2, Shield, Sparkles } from 'lucide-vue-next'

const toast = useToastStore()
const { t } = useI18n()
const data = ref<SettingsResponse | null>(null)
const form = ref<Settings | null>(null)
const mappingText = ref('{}')
const loading = ref(false)
const saving = ref(false)

// password change
const currentPassword = ref('')
const newPassword = ref('')
const changingPassword = ref(false)

async function load() {
	loading.value = true
	try {
		const result = await api.getSettings()
		data.value = result
		form.value = { ...result.current, compaction_mode: result.current.compaction_mode || 'on', compaction_model: result.current.compaction_model || 'gpt-6-luna', switch_on_429: result.current.switch_on_429 ?? true, account_cooldown_seconds: result.current.account_cooldown_seconds ?? 60, max_attempts: result.current.max_attempts ?? 2 }
		mappingText.value = JSON.stringify(result.current.model_mappings ?? {}, null, 2)
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'failed to load settings')
	} finally {
		loading.value = false
	}
}

onMounted(load)

async function save() {
	if (!form.value) return
	saving.value = true
	try {
		let mappings: Record<string, string> = {}
		try {
			mappings = JSON.parse(mappingText.value || '{}')
		} catch {
			toast.error(t('settings.modelMappings') + ': JSON')
			saving.value = false
			return
		}
		const result = await api.putSettings({ ...form.value, model_mappings: mappings })
		form.value = { ...result.settings }
		toast.success(t('settings.saved'))
		await load()
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'save failed')
	} finally {
		saving.value = false
	}
}

async function changePassword() {
	if (!newPassword.value) return
	changingPassword.value = true
	try {
		await api.changePassword(currentPassword.value, newPassword.value)
		currentPassword.value = ''
		newPassword.value = ''
		toast.success(t('settings.passwordChanged'))
	} catch (err) {
		toast.error(err instanceof Error ? err.message : 'could not change the password')
	} finally {
		changingPassword.value = false
	}
}

const builtinUserAgent = computed(() => data.value?.current.user_agent || 'pi (linux 6.1.0; x64)')
const compactionModels = computed(() => [...new Set(['gpt-6-luna', form.value?.compaction_model, form.value?.default_model, ...(data.value?.compaction_models ?? [])].filter((model): model is string => !!model))].sort())
</script>

<template>
	<div class="page-view">
		<PageHeader :title="t('settings.title')" :subtitle="t('settings.subtitle')">
			<button class="btn" :disabled="loading || saving" @click="load"><RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />{{ t('settings.reload') }}</button>
			<button class="btn btn-primary" :disabled="saving || loading || !form" @click="save">
				<Save class="h-4 w-4" />{{ saving ? t('settings.saving') : t('settings.save') }}
			</button>
		</PageHeader>

		<div v-if="form" class="page-content settings-grid" :aria-busy="loading || saving">
			<!-- Left column -->
			<div class="settings-stack">
				<div class="card panel-content">
					<h2 class="section-title"><Network aria-hidden="true" />{{ t('settings.transportSection') }}</h2>
					<div class="space-y-3">
						<div>
							<label class="label" for="s-transport">{{ t('settings.defaultTransport') }}</label>
							<select id="s-transport" v-model="form.upstream_transport" class="input" aria-describedby="s-transport-hint">
								<option v-for="transport in data?.transports ?? []" :key="transport" :value="transport">{{ transportLabel(transport) }}</option>
								<option v-if="!data?.transports.includes(form.upstream_transport)" :value="form.upstream_transport">{{ transportLabel(form.upstream_transport) }}</option>
							</select>
							<p id="s-transport-hint" class="setting-option-hint">{{ transportDescription(form.upstream_transport) }}</p>
							<p class="setting-option-hint">{{ t('ui.options.transportPriority') }}</p>
						</div>
						<div>
							<label class="label" for="s-ua">{{ t('settings.userAgentOverride') }}</label>
							<input id="s-ua" v-model="form.user_agent" class="input font-mono" placeholder="pi (linux 6.1.0; x64)" />
							<p class="mt-1 text-[11px] text-[color:var(--color-ink-faint)]">
								{{ t('settings.userAgentHint') }} <code class="font-mono">{{ builtinUserAgent }}</code>
							</p>
						</div>
					</div>
				</div>

				<div class="card panel-content">
					<h2 class="section-title"><Settings2 aria-hidden="true" />{{ t('settings.accountsSection') }}</h2>
					<div class="grid gap-4 sm:grid-cols-2">
						<div>
							<label class="label" for="s-strategy">{{ t('settings.defaultStrategy') }}</label>
							<select id="s-strategy" v-model="form.default_strategy" class="input" aria-describedby="s-strategy-hint">
								<option v-for="strategy in data?.strategies ?? []" :key="strategy" :value="strategy">{{ strategyLabel(strategy) }}</option>
								<option v-if="!data?.strategies.includes(form.default_strategy)" :value="form.default_strategy">{{ strategyLabel(form.default_strategy) }}</option>
							</select>
							<p id="s-strategy-hint" class="setting-option-hint">{{ strategyDescription(form.default_strategy) }}</p>
							<p class="setting-option-hint">{{ t('ui.options.strategyPriority') }}</p>
						</div>
						<div>
							<Toggle :model-value="form.switch_on_429 ?? true" @update:model-value="form.switch_on_429 = $event" :label="t('retry429.switch')" />
							<p class="setting-option-hint">{{ t('retry429.switchHint') }}</p>
						</div>
						<div><label class="label" for="s-cooldown-429">{{ t('retry429.cooldown') }}</label><input id="s-cooldown-429" v-model.number="form.account_cooldown_seconds" type="number" min="0" max="604800" class="input" /></div>
						<div><label class="label" for="s-attempts-429">{{ t('retry429.attempts') }}</label><input id="s-attempts-429" v-model.number="form.max_attempts" type="number" min="1" max="20" class="input" /></div>
						<div>
							<label class="label" for="s-conc">{{ t('settings.maxConcurrent') }}</label>
							<input id="s-conc" v-model.number="form.max_concurrent_per_account" type="number" min="1" class="input" />
						</div>
						<div>
							<label class="label" for="s-refresh">{{ t('settings.refreshMargin') }}</label>
							<input id="s-refresh" v-model.number="form.refresh_margin_seconds" type="number" min="60" class="input" />
						</div>
						<div>
							<label class="label" for="s-proxy">{{ t('settings.defaultProxy') }}</label>
							<input id="s-proxy" v-model="form.default_proxy_url" class="input font-mono" placeholder="empty = direct" />
						</div>
					</div>
				</div>

				<div class="card panel-content">
					<h2 class="section-title"><Sparkles aria-hidden="true" />{{ t('settings.modelsSection') }}</h2>
					<div class="grid gap-4 sm:grid-cols-2">
						<div>
							<label class="label" for="s-compaction-mode">{{ t('settings.compactionMode') }}</label>
							<select id="s-compaction-mode" v-model="form.compaction_mode" class="input" aria-describedby="s-compaction-hint">
								<option value="auto">{{ t('settings.compactionAuto') }}</option>
									<option value="on">{{ t('settings.compactionOn') }}</option>
									<option value="off">{{ t('settings.compactionOff') }}</option>
								</select>
							<p id="s-compaction-hint" class="setting-option-hint">{{ t('settings.compactionHint') }}</p>
						</div>
						<div>
							<label class="label" for="s-compaction-model">{{ t('settings.compactionModel') }}</label>
							<input id="s-compaction-model" v-model="form.compaction_model" list="compaction-models" class="input font-mono" placeholder="gpt-6-luna" :disabled="form.compaction_mode === 'off'" aria-describedby="s-compaction-model-hint" />
							<datalist id="compaction-models"><option v-for="model in compactionModels" :key="model" :value="model" /></datalist>
							<p id="s-compaction-model-hint" class="setting-option-hint">{{ t('settings.compactionModelHint') }}</p>
							<p v-if="data?.compaction_models_error" class="setting-option-hint">{{ t('settings.compactionCatalogError') }}</p>
						</div>
						<div>
							<label class="label" for="s-model">{{ t('settings.defaultModel') }}</label>
							<input id="s-model" v-model="form.default_model" class="input font-mono" :placeholder="data?.current.default_model || 'YOUR_MODEL'" />
						</div>
						<div>
							<label class="label" for="s-effort">{{ t('settings.reasoningEffort') }}</label>
							<select id="s-effort" v-model="form.reasoning_default_effort" class="input">
								<option value="">{{ t('settings.reasoningOmit') }}</option>
								<option value="none">none</option>
								<option value="minimal">minimal</option>
								<option value="low">low</option>
								<option value="medium">medium</option>
								<option value="high">high</option>
								<option value="xhigh">xhigh</option>
								<option value="max">max</option>
							</select>
						</div>
						<div class="sm:col-span-2">
							<label class="label" for="s-mappings">{{ t('settings.modelMappings') }}</label>
							<textarea id="s-mappings" v-model="mappingText" class="input font-mono" rows="3" spellcheck="false" />
							<p class="mt-1 text-[11px] text-[color:var(--color-ink-faint)]">
								{{ t('settings.modelMappingsHint') }} <code class="font-mono">{{ JSON.stringify({ 'client-model-alias': form.default_model || 'YOUR_MODEL' }) }}</code>
							</p>
						</div>
					</div>
				</div>

				<div class="card panel-content">
					<h2 class="section-title"><Shield aria-hidden="true" />{{ t('settings.passwordSection') }}</h2>
					<p class="mb-3 text-[12px] text-[color:var(--color-ink-muted)]">
						{{ t('settings.passwordHint') }}
					</p>
					<div class="grid gap-4 sm:grid-cols-2">
						<div>
							<label class="label" for="s-cur">{{ t('settings.currentPassword') }}</label>
							<input id="s-cur" v-model="currentPassword" type="password" class="input" />
						</div>
						<div>
							<label class="label" for="s-new">{{ t('settings.newPassword') }}</label>
							<input id="s-new" v-model="newPassword" type="password" class="input" />
						</div>
					</div>
					<button class="btn mt-3" :disabled="changingPassword || !currentPassword || !newPassword" @click="changePassword">
						{{ changingPassword ? t('settings.updating') : t('settings.updatePassword') }}
					</button>
				</div>
			</div>

			<!-- Right column -->
			<div class="settings-stack">
				<div class="card panel-content">
					<h2 class="section-title"><Activity aria-hidden="true" />{{ t('settings.captureSection') }}</h2>
					<div class="space-y-3">
						<div class="flex items-center justify-between">
							<span class="text-[13px]">{{ t('settings.captureEnabled') }}</span>
							<Toggle v-model="form.capture_enabled" :aria-label="t('settings.captureEnabled')" />
						</div>
						<div>
							<label class="label" for="s-limit">{{ t('settings.captureLimit') }}</label>
							<input id="s-limit" v-model.number="form.capture_limit" type="number" min="1" class="input" />
							<p class="mt-1 text-[11px] text-[color:var(--color-ink-faint)]">{{ t('settings.captureLimitHint') }}</p>
						</div>
						<div class="border-t border-[color:var(--color-line)] pt-3 text-[11px] text-[color:var(--color-ink-faint)]">
							<div class="flex justify-between py-0.5">
								<span>{{ t('settings.persistToSqlite') }}</span>
								<span>{{ data?.static.capture_persist ? t('common.yes') : t('common.no') }}</span>
							</div>
							<div class="flex justify-between py-0.5">
								<span>{{ t('settings.maxBytesPerRecord') }}</span>
								<span>{{ formatBytes(data?.static.max_bytes_per_record ?? 0) }}</span>
							</div>
						</div>
					</div>
				</div>

				<div class="card panel-content">
					<h2 class="section-title"><Fingerprint aria-hidden="true" />{{ t('settings.fingerprintSection') }}</h2>
					<dl class="space-y-2 text-[12px]">
						<div>
							<dt class="text-[11px] text-[color:var(--color-ink-faint)] uppercase">{{ t('settings.baseUrl') }}</dt>
							<dd class="font-mono break-all">{{ data?.static.upstream_base_url }}</dd>
						</div>
						<div class="flex items-center justify-between">
							<dt class="text-[11px] text-[color:var(--color-ink-faint)] uppercase">{{ t('settings.sseZstd') }}</dt>
							<dd>
								<Badge :tone="data?.static.sse_zstd ? 'success' : 'neutral'">
									{{ data?.static.sse_zstd ? t('common.on') : t('common.off') }}
								</Badge>
							</dd>
						</div>
						<div class="flex items-center justify-between">
							<dt class="text-[11px] text-[color:var(--color-ink-faint)] uppercase">{{ t('settings.originator') }}</dt>
							<dd class="font-mono">pi</dd>
						</div>
						<div class="flex items-center justify-between">
							<dt class="text-[11px] text-[color:var(--color-ink-faint)] uppercase">{{ t('settings.callbackPort') }}</dt>
							<dd class="font-mono">{{ data?.static.oauth_callback_port }}</dd>
						</div>
						<div class="flex items-center justify-between">
							<dt class="text-[11px] text-[color:var(--color-ink-faint)] uppercase">{{ t('settings.timezone') }}</dt>
							<dd class="font-mono">{{ data?.static.timezone }}</dd>
						</div>
					</dl>
					<p class="mt-3 border-t border-[color:var(--color-line)] pt-3 text-[11px] text-[color:var(--color-ink-faint)]">
						{{ t('settings.fingerprintNote') }}
					</p>
				</div>

				<div class="card panel-content">
					<h2 class="section-title"><Database aria-hidden="true" />{{ t('settings.storage') }}</h2>
					<p class="font-mono text-[11px] break-all text-[color:var(--color-ink-muted)]">{{ data?.static.database }}</p>
				</div>
			</div>
		</div>
	</div>
</template>

<style scoped>
.settings-stack, .settings-stack .grid > div { min-width: 0; }
.settings-stack select { min-width: 0; max-width: 100%; text-overflow: ellipsis; }
.setting-option-hint { margin: 6px 0 0; color: var(--color-ink-faint); font-size: 11px; line-height: 1.5; overflow-wrap: anywhere; }
</style>
