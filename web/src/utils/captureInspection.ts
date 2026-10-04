import type {Capture, CaptureFrame, CaptureHeader} from '../api/types'
import {equalPayloads, normalizeHeaders} from './captureDiff'

export interface CaptureSnapshot {
    available: boolean;
    value?: unknown
}

export interface CaptureRow {
    key: string;
    frame?: CaptureFrame;
    before?: CaptureFrame;
    after?: CaptureFrame
}

export type HopProtocol = 'sse' | 'ws' | 'http' | 'unknownProtocol'
const missing = (): CaptureSnapshot => ({available: false})
const own = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key)

export function frameSnapshot(frame?: CaptureFrame): CaptureSnapshot {
    if (!frame) return missing()
    if (own(frame, 'data') && frame.data !== undefined) return {available: true, value: frame.data}
    if (typeof frame.text === 'string') return {available: true, value: frame.text}
    return missing()
}

export function payloadPreview(value: unknown, limit = 100_000) {
    try {
        const text = (typeof value === 'string' ? value : JSON.stringify(value, null, 2)) ?? ''
        return {text: text.slice(0, limit), limited: text.length > limit}
    } catch {
        return {text: '', limited: true}
    }
}

export function payloadObject(value: unknown): Record<string, unknown> | undefined {
    try {
        const parsed = typeof value === 'string' ? JSON.parse(value) : value
        return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : undefined
    } catch {
        return undefined
    }
}

function isHeaders(value: unknown): value is CaptureHeader[] {
    return Array.isArray(value) && value.every(item => item && typeof item.name === 'string' && typeof item.value === 'string')
}

function headerSnapshot(value: unknown): CaptureSnapshot {
    return isHeaders(value) ? {available: true, value: normalizeHeaders(value)} : missing()
}

export function requestSnapshots(capture: Capture) {
    const frames = capture.response_frames ?? []
    const ordinary = frames.find(frame => frame.dir === 'client_in' && frame.kind === 'request_body')
    const socket = frames.find(frame => frame.dir === 'client_in' && frame.kind === 'ws_frame' && frame.type === 'response.create')
    const head = frames.find(frame => frame.dir === 'client_in' && frame.kind === 'handshake_request')
    const beforeBody = frameSnapshot(socket ?? ordinary)
    const afterBody: CaptureSnapshot = capture.request_body ? {available: true, value: capture.request_body} : missing()
    const beforeHeaders = headerSnapshot(head?.data)
    const afterHeaders = capture.request_headers?.length ? headerSnapshot(capture.request_headers) : missing()
    const consumed = new Set<number>()
    for (const frame of [ordinary, socket, head]) if (frame) consumed.add(frame.seq)
    // OnRequestBody already holds the actual response.create envelope. Suppress
    // only its matching duplicate; retries or different outgoing frames remain.
    const duplicate = frames.find(frame => frame.dir === 'out' && frame.kind === 'ws_frame' && frame.type === 'response.create'
        && afterBody.available && frameSnapshot(frame).available && equalPayloads(frameSnapshot(frame).value, afterBody.value))
    if (duplicate && !capture.truncated) consumed.add(duplicate.seq)
    return {beforeBody, afterBody, beforeHeaders, afterHeaders, consumed}
}

export function responseHeaderSnapshots(capture: Capture) {
    const recorded = (capture.response_frames ?? []).filter(frame => frame.dir === 'client_out' && frame.kind === 'handshake_response')
    const frame = recorded[recorded.length - 1]
    const data = payloadObject(frame?.data)
    return {
        before: capture.response_headers?.length ? headerSnapshot(capture.response_headers) : missing(),
        after: headerSnapshot(data?.headers),
        status: typeof data?.status === 'number' ? data.status : undefined,
    }
}

function isResponse(frame: CaptureFrame) {
    return ['sse_event', 'ws_frame', 'http_response', 'http_error_body'].includes(frame.kind)
}

function related(before: CaptureFrame, after: CaptureFrame): boolean {
    const left = frameSnapshot(before), right = frameSnapshot(after)
    if (!left.available || !right.available) return false
    if (equalPayloads(left.value, right.value)) return true
    if (before.type && before.type === after.type && before.kind !== 'http_error_body') return true
    if (before.kind === 'http_error_body' && after.type === 'error' && (after.kind === 'http_response' || after.kind === 'ws_frame')) return true
    const failures = ['error', 'response.failed', 'response.incomplete']
    if (failures.includes(before.type ?? '') && failures.includes(after.type ?? '')) return true
    // stream:false unwraps the response object from the final upstream event.
    const wrapped = payloadObject(left.value)
    return after.kind === 'http_response' && wrapped !== undefined && own(wrapped, 'response') && equalPayloads(wrapped.response, right.value)
}

export function captureRows(capture: Capture, compact: boolean): CaptureRow[] {
    const frames = capture.response_frames ?? []
    if (!compact) return frames.map((frame, index) => ({key: `${index}:${frame.seq}`, frame}))
    const {consumed} = requestSnapshots(capture)
    const rows: CaptureRow[] = []
    let pending = -1
    for (const [index, frame] of frames.entries()) {
        if (consumed.has(frame.seq)) continue
        if (frame.dir === 'client_out' && frame.kind === 'handshake_response') continue
        if (frame.dir === 'client_out' && isResponse(frame)) {
            const candidate = rows[pending]?.frame
            if (!capture.truncated && candidate?.dir === 'in' && related(candidate, frame)) {
                rows[pending] = {key: rows[pending]!.key, before: candidate, after: frame}
                pending = -1
                continue
            }
            pending = -1
        }
        rows.push({key: `${index}:${frame.seq}`, frame})
        if (frame.dir === 'in') pending = isResponse(frame) ? rows.length - 1 : -1
        else if (frame.dir === 'client_in' || frame.dir === 'out') pending = -1
    }
    return rows
}

function isSocket(value: string) {
    return value === 'ws' || value.startsWith('websocket')
}

export function hopProtocol(capture: Capture, side: 'upstream' | 'client'): HopProtocol {
    const dir = side === 'upstream' ? 'in' : 'client_out'
    const frames = (capture.response_frames ?? []).filter(frame => frame.dir === dir)
    if (frames.some(frame => frame.kind === 'sse_event')) return 'sse'
    if (frames.some(frame => frame.kind === 'ws_frame' || frame.kind === 'ws_binary')) return 'ws'
    if (side === 'client' && frames.some(frame => frame.kind === 'http_response')) return 'http'
    const configured = side === 'client' ? capture.client_transport : capture.upstream_transport
    if (isSocket(configured)) return 'ws'
    if (side === 'client' && payloadObject(requestSnapshots(capture).beforeBody.value)?.stream === false) return 'http'
    if (configured === 'sse') return 'sse'
    return 'unknownProtocol'
}

const streamPreviewLimit = 200_000

export function streamSnapshot(capture: Capture, side: 'upstream' | 'client') {
    const frames = (capture.response_frames ?? []).filter(frame => frame.dir === (side === 'upstream' ? 'in' : 'client_out'))
    const events = frames.filter(frame => frame.kind === 'sse_event')
    const socket = frames.filter(frame => frame.kind === 'ws_frame' || frame.kind === 'ws_binary')
    const http = frames.filter(frame => frame.kind === 'http_error_body' || frame.kind === 'http_response')
    let text = '', limited = false
    const recordedText = side === 'client' ? capture.response_text ?? '' : ''
    if (recordedText) {
        text = recordedText.slice(0, streamPreviewLimit)
        limited = recordedText.length > streamPreviewLimit
    } else {
        for (const frame of events) {
            const snapshot = frameSnapshot(frame)
            if (!snapshot.available) continue
            const raw = typeof snapshot.value === 'string' ? snapshot.value : JSON.stringify(snapshot.value)
            if (typeof raw !== 'string') continue
            const formatted = raw.replace(/\r\n?/g, '\n').split('\n').map(line => `data: ${line}`).join('\n') + '\n\n'
            const room = streamPreviewLimit - text.length
            text += formatted.slice(0, room)
            if (formatted.length > room) {
                limited = true;
                break
            }
        }
    }
    return {text, limited, reconstructed: !recordedText, socket, http, frames, protocol: hopProtocol(capture, side)}
}
