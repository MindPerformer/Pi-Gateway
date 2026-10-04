<script setup lang="ts">
import { computed } from 'vue'
import type { Capture } from '../../api/types'
import { frameSnapshot, payloadObject, payloadPreview as preview, requestSnapshots, responseHeaderSnapshots, streamSnapshot } from '../../utils/captureInspection'
import JsonViewer from '../JsonViewer.vue'
import Badge from '../Badge.vue'
import { useCaptureLocale } from './captureLocale'

const props = defineProps<{ capture: Capture; side: 'upstream' | 'client' }>()
const { c } = useCaptureLocale()
const snapshot = computed(() => streamSnapshot(props.capture, props.side))
const direction = computed(() => c(props.side === 'upstream' ? 'upstreamIn' : 'clientOut'))
const status = computed(() => props.side === 'upstream' ? props.capture.status : responseHeaderSnapshots(props.capture).status)
const explanation = computed(() => {
	if (snapshot.value.text) return ''
	if (props.side === 'upstream' && props.capture.status >= 400 && snapshot.value.http.length) return c('noSseHttpError', { status: props.capture.status })
	if (props.side === 'client' && !snapshot.value.frames.some(frame => ['http_response', 'ws_frame', 'ws_binary', 'sse_event'].includes(frame.kind))) return c('noClientResponse')
	if (snapshot.value.protocol === 'ws') return c('noSseWs')
	if (props.side === 'client' && payloadObject(requestSnapshots(props.capture).beforeBody.value)?.stream === false) return c('noSseAggregate')
	if (props.side === 'client' && snapshot.value.http.length && (status.value ?? 0) >= 400) return c('noSseClientError')
	return c('noSseRecorded')
})
</script>

<template>
	<section class="stream-panel card" :data-side="side">
		<header><h3>{{ direction }}</h3><Badge tone="info">{{ c(snapshot.protocol) }}</Badge><Badge v-if="status">{{ c('status', { status }) }}</Badge><small v-else>{{ c('unrecorded') }}</small></header>
		<p v-if="explanation" class="stream-explanation" role="status">{{ explanation }}</p>
		<template v-if="snapshot.text">
			<p class="stream-hint">{{ c(side === 'upstream' || snapshot.reconstructed ? 'reconstructed' : 'clientSseNote') }}</p>
			<JsonViewer :value="snapshot.text" raw :label="c(side === 'upstream' ? 'upstreamSse' : 'clientSse')" max-height="38rem" />
			<p v-if="snapshot.limited" class="stream-hint">{{ c('limited') }}</p>
		</template>
		<div v-if="snapshot.http.length" class="stream-messages">
			<h4>{{ direction }} · {{ c(side === 'upstream' ? 'capturedErrors' : 'capturedHttp') }}</h4>
			<div v-for="frame in snapshot.http.slice(0, 50)" :key="frame.seq"><JsonViewer :value="preview(frameSnapshot(frame).value).text" :label="`${direction} · ${frame.type || c('httpResponse')}`" max-height="28rem" /><p v-if="preview(frameSnapshot(frame).value).limited" class="stream-hint">{{ c('limited') }}</p></div>
		</div>
		<div v-if="snapshot.socket.length" class="stream-messages">
			<h4>{{ direction }} · {{ c('capturedWs') }}</h4>
			<div v-for="frame in snapshot.socket.slice(0, 50)" :key="frame.seq"><JsonViewer :value="preview(frameSnapshot(frame).value).text" :label="`${direction} · ${frame.type || c('wsFrame')}`" max-height="24rem" /><p v-if="preview(frameSnapshot(frame).value).limited" class="stream-hint">{{ c('limited') }}</p></div>
		</div>
		<p v-if="snapshot.socket.length > 50 || snapshot.http.length > 50" class="stream-hint">{{ c('limited') }}</p>
	</section>
</template>

<style scoped>
.stream-panel { display: grid; grid-template-columns: minmax(0, 1fr); align-content: start; gap: 12px; padding: 14px; min-width: 0; }
.stream-panel > header { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }.stream-panel h3 { font-size: 13px; font-weight: 650; margin: 0; }.stream-panel small { color: var(--color-ink-faint); }
.stream-explanation { margin: 0; font-size: 12px; line-height: 1.65; padding: 10px; border-radius: 6px; border: 1px solid var(--color-line); background: var(--color-surface-2); overflow-wrap: anywhere; }
.stream-hint { margin: 0; color: var(--color-ink-muted); font-size: 11px; line-height: 1.6; }
.stream-messages { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; min-width: 0; }.stream-messages > div { min-width: 0; }.stream-messages h4 { font-size: 12px; margin: 0; font-weight: 600; overflow-wrap: anywhere; }
.stream-panel :deep(.code-header > span) { min-width: 0; overflow-wrap: anywhere; }
@media (max-width: 390px) { .stream-panel { padding: 10px; } }
</style>
