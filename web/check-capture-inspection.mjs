import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'

function compile(name) {
    return ts.transpileModule(readFileSync(new URL(`./src/utils/${name}.ts`, import.meta.url), 'utf8'), {
        compilerOptions: {target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020},
    }).outputText
}

const moduleURL = source => `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`
const diffURL = moduleURL(compile('captureDiff'))
const {
    frameSnapshot,
    requestSnapshots,
    responseHeaderSnapshots,
    captureRows,
    hopProtocol,
    streamSnapshot,
    payloadPreview
} = await import(
    moduleURL(compile('captureInspection').replace("from './captureDiff'", `from '${diffURL}'`)),
    )
const headers = [{name: 'Content-Type', value: 'application/json'}]
const event = {type: 'response.completed', response: {id: 'response-local', output: []}}
const frame = (seq, dir, kind, data, type = '') => ({seq, dir, kind, type, data, at_ms: seq, bytes: 20})
const capture = (frames = [], extra = {}) => ({
    client_transport: 'sse', upstream_transport: 'sse', request_body: '', response_text: '',
    request_headers: [], response_headers: [], response_frames: frames, truncated: false, status: 200, ...extra,
})
let checked = 0

function check(name, fn) {
    fn();
    checked++;
    console.log(`PASS: ${name}`)
}

check('false/null/empty payloads remain recorded; absent payloads do not', () => {
    for (const data of [null, false, 0, '', {}, []]) assert.deepEqual(frameSnapshot({data}), {
        available: true,
        value: data
    })
    assert.deepEqual(frameSnapshot(), {available: false})
    assert.deepEqual(frameSnapshot({}), {available: false})
    assert.deepEqual(frameSnapshot({text: ''}), {available: true, value: ''})
})
check('old records never substitute the upstream request for the client request', () => {
    const requests = requestSnapshots(capture([], {
        request_body: '{"input":"upstream only"}',
        request_headers: headers
    }))
    assert.equal(requests.beforeBody.available, false)
    assert.equal(requests.beforeHeaders.available, false)
    assert.equal(requests.afterBody.available, true)
    assert.equal(requests.afterHeaders.available, true)
})
check('original and gateway request headers and payloads keep their own directions', () => {
    const original = {input: 'hello'}
    const requests = requestSnapshots(capture([
        frame(0, 'client_in', 'handshake_request', [{name: 'User-Agent', value: 'curl/8.8.0'}]),
        frame(1, 'client_in', 'request_body', original),
    ], {request_body: '{"input":"hello","stream":true}', request_headers: [{name: 'User-Agent', value: 'pi'}]}))
    assert.deepEqual(requests.beforeBody.value, original)
    assert.match(requests.afterBody.value, /stream/)
    assert.equal(requests.beforeHeaders.value[0].value, 'curl/8.8.0')
    assert.equal(requests.afterHeaders.value[0].value, 'pi')
})
check('WS uses the original response.create envelope and merges only its exact outgoing copy', () => {
    const body = {input: 'hello'}
    const envelope = {type: 'response.create', ...body}
    const record = capture([
        frame(0, 'client_in', 'request_body', body),
        frame(1, 'client_in', 'ws_frame', envelope, 'response.create'),
        frame(2, 'out', 'ws_frame', envelope, 'response.create'),
        frame(3, 'out', 'ws_frame', {...envelope, input: 'retry'}, 'response.create'),
    ], {request_body: JSON.stringify(envelope), client_transport: 'ws', upstream_transport: 'websocket-cached'})
    assert.deepEqual(requestSnapshots(record).beforeBody.value, envelope)
    assert.deepEqual(captureRows(record, true).map(row => row.frame.seq), [3])
    assert.equal(captureRows(record, false).length, 4)
    assert.equal(hopProtocol(record, 'client'), 'ws')
})
check('response headers/status are not borrowed from the upstream when client headers were not captured', () => {
    const old = capture([], {response_headers: headers, status: 403})
    assert.equal(responseHeaderSnapshots(old).before.available, true)
    assert.equal(responseHeaderSnapshots(old).after.available, false)
    assert.equal(responseHeaderSnapshots(old).status, undefined)
    const next = capture([frame(1, 'client_out', 'handshake_response', {status: 502})], old)
    next.response_frames = [frame(1, 'client_out', 'handshake_response', {status: 502})]
    assert.equal(responseHeaderSnapshots(next).status, 502)
    assert.equal(responseHeaderSnapshots(next).after.available, false)
    next.response_frames.push(frame(2, 'client_out', 'handshake_response', {status: 200, headers: []}))
    assert.deepEqual(responseHeaderSnapshots(next).after, {available: true, value: []})
})
check('unchanged response events merge once across the client header record', () => {
    const record = capture([
        frame(0, 'in', 'sse_event', event, event.type),
        frame(1, 'client_out', 'handshake_response', {status: 200, headers}),
        frame(2, 'client_out', 'sse_event', event, event.type),
    ])
    const rows = captureRows(record, true)
    assert.equal(rows.length, 1)
    assert.equal(rows[0].before.dir, 'in')
    assert.equal(rows[0].after.dir, 'client_out')
    assert.equal(captureRows(record, false).length, 3)
})
check('HTTP rejections pair the upstream error with the actual HTTP or WS client error', () => {
    for (const kind of ['http_response', 'ws_frame']) {
        const record = capture([
            frame(0, 'in', 'http_error_body', {upstream_only: true}),
            frame(1, 'client_out', kind, {error: {message: 'gateway error'}}, 'error'),
        ], {status: 403})
        const rows = captureRows(record, true)
        assert.equal(rows.length, 1)
        assert.deepEqual(rows[0].before.data, {upstream_only: true})
        assert.equal(rows[0].after.data.error.message, 'gateway error')
        assert.equal(streamSnapshot(record, 'upstream').text, '')
        assert.equal(streamSnapshot(record, 'client').text, '')
    }
})
check('changed response events and aggregated terminal response can be compared', () => {
    const before = frame(0, 'in', 'sse_event', event, event.type)
    const modified = frame(1, 'client_out', 'sse_event', {...event, injected: true}, event.type)
    assert.equal(captureRows(capture([before, modified]), true).length, 1)
    const aggregate = frame(1, 'client_out', 'http_response', event.response, 'response')
    const rows = captureRows(capture([before, aggregate]), true)
    assert.equal(rows.length, 1)
    assert.deepEqual(rows[0].after.data, event.response)
})
check('unrelated, incomplete and truncated frames are never silently merged', () => {
    const before = frame(0, 'in', 'sse_event', event, event.type)
    const after = frame(2, 'client_out', 'sse_event', event, event.type)
    assert.equal(captureRows(capture([before, after], {truncated: true}), true).length, 2)
    assert.equal(captureRows(capture([before, frame(1, 'out', 'ws_frame', {retry: true}), after]), true).length, 3)
    assert.equal(captureRows(capture([before, frame(1, 'client_out', 'http_response', {unrelated: true}, 'other')]), true).length, 2)
    assert.equal(captureRows(capture([before, frame(1, 'client_out', 'sse_event', undefined, event.type)]), true).length, 2)
})
check('old SSE text belongs to the client only; upstream SSE is explicitly reconstructed', () => {
    const record = capture([frame(0, 'in', 'sse_event', event, event.type)], {response_text: 'data: client-only\n\n'})
    const before = streamSnapshot(record, 'upstream'), after = streamSnapshot(record, 'client')
    assert.equal(before.reconstructed, true)
    assert.match(before.text, /response.completed/)
    assert.doesNotMatch(before.text, /client-only/)
    assert.equal(after.reconstructed, false)
    assert.equal(after.text, record.response_text)
})
check('WS to SSE uses independent per-hop protocols and response payloads', () => {
    const record = capture([
        frame(0, 'in', 'ws_frame', event, event.type),
        frame(1, 'client_out', 'sse_event', event, event.type),
    ], {upstream_transport: 'websocket-cached'})
    assert.equal(hopProtocol(record, 'upstream'), 'ws')
    assert.equal(hopProtocol(record, 'client'), 'sse')
    assert.equal(streamSnapshot(record, 'upstream').text, '')
    assert.equal(streamSnapshot(record, 'upstream').socket.length, 1)
    assert.match(streamSnapshot(record, 'client').text, /data: /)
    assert.equal(captureRows(record, true).length, 1)
})
check('stream:false is HTTP even without a saved response', () => {
    const record = capture([frame(0, 'client_in', 'request_body', {stream: false})])
    assert.equal(hopProtocol(record, 'client'), 'http')
    assert.equal(streamSnapshot(record, 'client').text, '')
})
check('large SSE previews are bounded and explicitly marked', () => {
    const text = 'data: ' + 'x'.repeat(250_000)
    const client = streamSnapshot(capture([], {response_text: text}), 'client')
    assert.equal(client.text.length, 200_000)
    assert.equal(client.limited, true)
    const upstream = streamSnapshot(capture([frame(0, 'in', 'sse_event', {text})]), 'upstream')
    assert.equal(upstream.text.length, 200_000)
    assert.equal(upstream.limited, true)
})
check('unknown future records remain visible and preserve order', () => {
    const record = capture([frame(3, 'in', '__proto__', 'unknown'), frame(4, 'in', 'future_event', 'visible')])
    assert.deepEqual(captureRows(record, true).map(row => row.frame.seq), [3, 4])
})
check('individual payload previews signal truncation without changing small JSON', () => {
    assert.deepEqual(payloadPreview({ok: true}), {text: '{\n  "ok": true\n}', limited: false})
    assert.deepEqual(payloadPreview('x'.repeat(100_001)), {text: 'x'.repeat(100_000), limited: true})
    assert.deepEqual(payloadPreview(''), {text: '', limited: false})
})
check('binary WebSocket frames still identify the protocol', () => {
    const record = capture([frame(0, 'in', 'ws_binary', 'bytes')], {upstream_transport: ''})
    assert.equal(hopProtocol(record, 'upstream'), 'ws')
    assert.equal(streamSnapshot(record, 'upstream').socket.length, 1)
})
console.log(`PASS: ${checked} capture inspection checks`)
