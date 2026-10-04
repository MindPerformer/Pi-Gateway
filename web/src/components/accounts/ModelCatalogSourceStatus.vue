<script setup lang="ts">
import { computed } from 'vue'
import type { Account, ModelCatalog } from '../../api/types'
import { formatTime } from '../../stores/ui'
import { useAccountControls } from './accountControlsLocale'
import { modelCatalogOriginNames, modelCatalogOrigins } from './useAccountModelCatalog'

const props = defineProps<{ account: Account; catalog?: ModelCatalog }>()
const { c } = useAccountControls()
const sources = computed(() => modelCatalogOrigins.map(origin => {
	const snapshot = props.catalog?.source_catalogs?.[origin]
	return {
		origin,
		name: modelCatalogOriginNames[origin],
		snapshot,
		codexNotLinked: origin === 'codex' && (!props.account.codex_linked || snapshot?.skip_reason === 'codex_not_linked'),
	}
}))
</script>

<template>
	<section class="source-catalogs" :aria-label="c('catalogSourceStatus', { name: account.name || account.email || c('unnamedAccount') })">
		<strong class="source-heading">{{ c('catalogSourceStatus', { name: account.name || account.email || c('unnamedAccount') }) }}</strong>
		<div class="source-grid">
			<div v-for="source in sources" :key="source.origin" class="source-card">
				<div class="source-title"><strong>{{ source.name }}</strong><span v-if="source.snapshot">{{ c('sourceModelCount', { count: source.snapshot.models.length }) }}</span></div>
				<template v-if="source.snapshot">
					<p>{{ source.snapshot.fetched_at ? c('cacheAt', { time: formatTime(source.snapshot.fetched_at) }) : c('neverFetched') }}</p>
					<p v-if="source.snapshot.attempted_at">{{ c('failedAt', { time: formatTime(source.snapshot.attempted_at) }) }}</p>
					<p v-if="source.snapshot.skipped">{{ c('sourceSkipped') }}<span v-if="source.snapshot.skip_reason && source.snapshot.skip_reason !== 'codex_not_linked'"> · {{ c('sourceSkipReason', { reason: source.snapshot.skip_reason }) }}</span></p>
					<p v-if="source.snapshot.error" class="source-warning" role="alert">{{ c('sourceError', { error: source.snapshot.error }) }}<span v-if="source.snapshot.fetched_at"> {{ c('sourceCachePreserved') }}</span></p>
				</template>
				<p v-else>{{ c('sourceSnapshotMissing') }}</p>
				<template v-if="source.codexNotLinked">
					<p>{{ c('codexNotLinked') }}</p>
					<p>{{ c('codexLinkHint') }}</p>
				</template>
			</div>
		</div>
	</section>
</template>

<style scoped>
.source-catalogs { display: grid; gap: 7px; min-width: 0; }
.source-heading { color: var(--color-ink-muted); font-size: 11px; overflow-wrap: anywhere; }
.source-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.source-card { display: grid; align-content: start; gap: 5px; min-width: 0; padding: 10px; border: 1px solid var(--color-line); border-radius: 8px; background: var(--color-canvas); }
.source-title { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 5px; }
.source-title strong { color: var(--color-ink-muted); font-size: 11px; }
.source-title span, .source-card p { margin: 0; color: var(--color-ink-faint); font-size: 10px; line-height: 1.6; overflow-wrap: anywhere; }
.source-card .source-warning { color: var(--color-warn); }
@media (max-width: 480px) { .source-grid { grid-template-columns: 1fr; } }
</style>
