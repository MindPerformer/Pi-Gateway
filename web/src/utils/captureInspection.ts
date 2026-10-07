import type {Capture, CaptureFrame, CaptureHeader, CaptureRuleChange, CaptureRuleTrace} from '../api/types'
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

// Adjacent events share a group only within the same direction and stream item.
export interface CaptureRowGroup {
    key: string;
    type: string;
    rows: CaptureRow[]
}

export function groupCaptureRows(rows: CaptureRow[]): CaptureRowGroup[] {
    const groups: CaptureRowGroup[] = []
    const signature = (row: CaptureRow) => {
        const frames = [row.frame, row.before, row.after].filter((frame): frame is CaptureFrame => Boolean(frame))
        if (!frames.length || frames.some(frame => !['sse_event', 'ws_frame'].includes(frame.kind) || !frame.type || /(?:error|failed|completed|incomplete|response\.create)$/.test(frame.type))) return null
        return JSON.stringify(frames.map(frame => {
            const body = payloadObject(frameSnapshot(frame).value)
            return [frame.dir, frame.kind, frame.type, body?.item_id, body?.output_index, body?.content_index]
        }))
    }
    let previous: string | null = null
    for (const row of rows) {
        const key = signature(row), last = groups[groups.length - 1]
        if (key && key === previous && last) last.rows.push(row)
        else groups.push({key: row.key, type: (row.before ?? row.frame ?? row.after)?.type ?? '', rows: [row]})
        previous = key
    }
    return groups
}

export function groupRuleTraces(capture: Capture) {
    const groups: {
        key: string;
        trace: CaptureRuleTrace;
        count: number;
        changes: number;
        steps: { key: string; trace: CaptureRuleTrace; count: number; index: number }[];
        status: CaptureRuleTrace
    }[] = []
    const rules = new Map<string, typeof groups[number]>()
    const steps = new Map<string, typeof groups[number]['steps'][number]>()
    const severity = (trace: CaptureRuleTrace) => trace.error || ['error', 'blocked'].includes(trace.status ?? '') ? 4 : trace.rolled_back ? 3 : isRuleChange(trace) ? 2 : 1
    for (const [index, trace] of ruleTraces(capture).entries()) {
        const key = JSON.stringify([trace.rule_id ?? trace.rule_name, trace.phase, trace.revision, trace.rules_version, trace.source, trace.source_kind])
        let group = rules.get(key)
        if (!group) {
            group = {key, trace, count: 0, changes: 0, steps: [], status: trace};
            groups.push(group);
            rules.set(key, group)
        }
        group.count++
        if (isRuleChange(trace)) group.changes += (trace.changes?.length ?? 0) + (trace.omitted_changes ?? 0)
        if (severity(trace) > severity(group.status)) group.status = trace
        // The same action stays one summary even when each event changes a
        // different number of fields. Its detail is explicitly a first sample.
        const stepKey = JSON.stringify([key, trace.action_id, trace.action_index, trace.action_type, trace.status, trace.rolled_back, trace.error, trace.event_type])
        const step = steps.get(stepKey)
        if (step) step.count++
        else {
            const value = {key: stepKey, trace, count: 1, index};
            group.steps.push(value);
            steps.set(stepKey, value)
        }
    }
    return groups
}

// Sources are selected only from stored execution traces, never inferred from a
// final payload diff. Missing event IDs cannot attribute a particular response.
export function ruleTraces(capture: Capture, phase?: string, eventID?: string): CaptureRuleTrace[] {
    const traces = Array.isArray(capture.rule_traces) ? capture.rule_traces : []
    return traces.filter(trace => trace && typeof trace === 'object'
        && (!phase || trace.phase === phase)
        && (eventID === undefined || trace.event_id === eventID))
}

export function isRuleChange(trace: CaptureRuleTrace): boolean {
    return !trace.rolled_back && (trace.status === 'changed' || trace.status === 'dropped')
}

export function ruleSources(capture: Capture, phase?: string, eventID?: string): CaptureRuleTrace[] {
    if (!phase && !eventID) return []
    return ruleTraces(capture, phase, eventID).filter(isRuleChange)
}

export function ruleChangeSnapshot(change: CaptureRuleChange, side: 'before' | 'after'): CaptureSnapshot {
    const exists = side === 'before' ? change.before_exists : change.after_exists
    // A known absent field is not a missing capture. Wrap the existence bit so
    // absent, explicit null, and a literal preview string remain distinguishable.
    if (exists === false) return {available: true, value: {exists: false}}
    if (exists !== true || !own(change, side)) return missing()
    return {available: true, value: {exists: true, value: change[side]}}
}

export function lastRuleWriters(capture: Capture): Set<string> {
    const result = new Set<string>()
    if (capture.truncated || capture.rules_trace_truncated || capture.rules_trace_omitted) return result
    const traces = ruleTraces(capture)
    if (traces.some(trace => trace.trace_truncated || trace.omitted_changes || trace.changes?.some(change => change.truncated))) return result
    const scopes = new Map<string, { paths: Set<string>; parents: Set<string> }>()
    for (let index = traces.length - 1; index >= 0; index--) {
        const trace = traces[index]!
        if (!isRuleChange(trace)) continue
        const scope = JSON.stringify([trace.phase, trace.event_id ?? ''])
        const later = scopes.get(scope) ?? {paths: new Set<string>(), parents: new Set<string>()}
        scopes.set(scope, later)
        const changes = trace.changes ?? []
        for (let changeIndex = changes.length - 1; changeIndex >= 0; changeIndex--) {
            const path = changes[changeIndex]!.path
            if (typeof path !== 'string') continue
            const parents = path.split('/').slice(0, -1).map((_, depth) => path.split('/').slice(0, depth + 1).join('/'))
            const overwritten = later.paths.has(path) || later.parents.has(path) || parents.some(parent => later.paths.has(parent))
            // Numeric paths can refer to different elements after array edits.
            if (!overwritten && !/\/(?:0|[1-9]\d*)(?:\/|$)/.test(path)) result.add(`${index}:${changeIndex}`)
            later.paths.add(path)
            for (const parent of parents) later.parents.add(parent)
        }
    }
    return result
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

export function payloadPreview(value: unknown, limit = Number.POSITIVE_INFINITY) {
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
    if (before.rule_event_id || after.rule_event_id) return Boolean(before.rule_event_id && after.rule_event_id && before.rule_event_id === after.rule_event_id)
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
    const eventRows = new Map<string, number>()
    const counts = new Map<string, { before: number; after: number }>()
    for (const frame of frames) {
        if (!frame.rule_event_id || !isResponse(frame)) continue
        const count = counts.get(frame.rule_event_id) ?? {before: 0, after: 0}
        if (frame.dir === 'in') count.before++
        if (frame.dir === 'client_out') count.after++
        counts.set(frame.rule_event_id, count)
    }
    let pending = -1
    for (const [index, frame] of frames.entries()) {
        if (consumed.has(frame.seq)) continue
        if (frame.dir === 'client_out' && frame.kind === 'handshake_response') continue
        if (frame.dir === 'client_out' && isResponse(frame)) {
            const id = frame.rule_event_id
            const count = id ? counts.get(id) : undefined
            const explicit = Boolean(id && count?.before === 1 && count.after === 1)
            const position = explicit ? eventRows.get(id!) ?? -1 : id ? -1 : pending
            const candidate = rows[position]?.frame
            if (candidate?.dir === 'in' && (explicit || (!capture.truncated && !capture.rules_trace_truncated)) && related(candidate, frame)) {
                rows[position] = {key: rows[position]!.key, before: candidate, after: frame}
                pending = -1
                continue
            }
            pending = -1
        }
        rows.push({key: `${index}:${frame.seq}`, frame})
        if (frame.dir === 'in' && isResponse(frame) && frame.rule_event_id) eventRows.set(frame.rule_event_id, rows.length - 1)
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

const streamPreviewLimit = Number.POSITIVE_INFINITY

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
