import assert from 'node:assert/strict'
import {build} from 'esbuild'

const bundled = await build({
    entryPoints: ['src/utils/ruleSimulation.ts'],
    bundle: true,
    write: false,
    platform: 'node',
    format: 'esm',
    logLevel: 'silent'
})
const m = await import(`data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].text).toString('base64')}`)
const frames = [{seq: 4, dir: 'in', kind: 'ws_frame', type: 'response.output_text.delta'},
    {seq: 0, dir: 'in', kind: 'sse_event', type: 'response.created'},
    {seq: 3, dir: 'client_out', kind: 'sse_event'}, {seq: 2, dir: 'in', kind: 'handshake_response'},
    {seq: 1, dir: 'internal', kind: 'rule_input'}]
const events = m.recordedRuleEvents({response_frames: frames})
assert.deepEqual(events.map(frame => frame.seq), [0, 4])
assert.equal(frames[0].seq, 4, 'selection does not reorder stored frames')
assert.equal(m.preferredRuleEvent(events), 4)
assert.equal(m.preferredRuleEvent(events, 0), 0, 'event zero is not missing')
assert.equal(m.preferredRuleEvent(events, 999), 4)
assert.equal(m.preferredRuleEvent([]), undefined)
for (const value of ['', '0', '-1', '1e2', '2.3', '9007199254740993', ['3'], null, undefined]) assert.equal(m.positiveCaptureID(value), undefined)
assert.equal(m.positiveCaptureID('42'), 42)
assert.equal(m.frameSequence('0'), 0)
assert.equal(m.frameSequence('0.0'), undefined)

const nulls = m.simulationSnapshots({before: null, result: {body: null}})
assert.deepEqual(nulls, {before: {available: true, value: null}, after: {available: true, value: null}})
assert.equal(m.simulationSnapshots({result: {}}).before.available, false)
assert.equal(m.simulationSnapshots({before: {}, result: {}}).after.available, false)
const diff = m.simulationChanges({a: null, b: false, c: 0, gone: true}, {a: false, b: 0, c: '', added: null})
assert.deepEqual(diff.changes.map(change => [change.path, change.operation]), [['/a', 'replace'], ['/b', 'replace'], ['/c', 'replace'], ['/gone', 'remove'], ['/added', 'add']])
assert.equal(diff.changes[3].afterExists, false)
assert.equal(diff.changes[4].afterExists, true)
assert.equal(diff.changes[4].after, null)
assert.deepEqual(m.simulationChanges({b: [1, 2], a: {}}, {a: {}, b: [1, 2]}).changes, [])
assert.equal(m.simulationChanges({'a/b~c': 1}, {'a/b~c': 2}).changes[0].path, '/a~1b~0c')
assert.equal(m.simulationChanges({'': 1}, {'': 2}).changes[0].path, '/')
assert.equal(m.simulationChanges({a: 1, b: 2}, {a: 2, b: 3}, 1).limited, true)
assert.equal(m.simulationChanges({a: 1, b: 2}, {a: 2, b: 3}, 1).changes.length, 1)
const special = JSON.parse('{"__proto__":null,"constructor":false}')
assert.deepEqual(m.simulationChanges({}, special).changes.map(change => change.path), ['/__proto__', '/constructor'])
assert.equal({}.polluted, undefined)
assert.equal(m.samplePreview({delta: 'hello'}), 'hello')
assert.equal(m.samplePreview(null), 'null')
assert.ok(m.samplePreview('a'.repeat(10000), 20).length <= 21)
console.log('Capture simulation selection, zero sequence, explicit null, bounded final diff and literal keys passed.')
