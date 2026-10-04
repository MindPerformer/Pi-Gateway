import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'

const source = readFileSync(new URL('./src/utils/captureDiff.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
    compilerOptions: {target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020, strict: true},
    reportDiagnostics: true,
    fileName: 'captureDiff.ts',
})
const errors = (compiled.diagnostics ?? []).filter((item) => item.category === ts.DiagnosticCategory.Error)
assert.equal(errors.length, 0, ts.formatDiagnosticsWithColorAndContext(errors, {
    getCurrentDirectory: () => process.cwd(),
    getCanonicalFileName: (name) => name,
    getNewLine: () => '\n',
}))
const {normalizeHeaders, equalPayloads, diffPayloads} = await import(
    `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString('base64')}`
    )

let checked = 0

function check(name, run) {
    try {
        run()
        checked++
    } catch (error) {
        throw new Error(`Capture diff check failed: ${name}`, {cause: error})
    }
}

function validate(diff) {
    assert.equal(typeof diff.equal, 'boolean')
    assert.equal(typeof diff.limited, 'boolean')
    assert.ok(diff.lines.length > 0 && diff.lines.length <= 1600)
    assert.equal(diff.added, diff.lines.filter((line) => line.kind === 'added').length)
    assert.equal(diff.removed, diff.lines.filter((line) => line.kind === 'removed').length)
    assert.ok(diff.lines.reduce((sum, line) => sum + line.text.length, 0) <= 2 * 65_536 + 100)
    for (const line of diff.lines) {
        assert.ok(['context', 'added', 'removed'].includes(line.kind))
        assert.equal(typeof line.text, 'string')
        assert.ok(line.text.length <= 2000)
        if (line.beforeLine !== undefined) assert.ok(Number.isInteger(line.beforeLine) && line.beforeLine > 0)
        if (line.afterLine !== undefined) assert.ok(Number.isInteger(line.afterLine) && line.afterLine > 0)
        if (line.kind === 'added') assert.equal(line.beforeLine, undefined)
        if (line.kind === 'removed') assert.equal(line.afterLine, undefined)
    }
    if (diff.equal) assert.equal(diff.added + diff.removed, 0)
    else assert.ok(diff.added + diff.removed > 0)
    return diff
}

function changed(before, after, limited = false) {
    assert.equal(equalPayloads(before, after), false)
    const diff = validate(diffPayloads(before, after))
    assert.equal(diff.equal, false)
    assert.equal(diff.limited, limited)
    return diff
}

function same(before, after, limited = false) {
    assert.equal(equalPayloads(before, after), true)
    const diff = validate(diffPayloads(before, after))
    assert.equal(diff.equal, true)
    assert.equal(diff.limited, limited)
    return diff
}

function textOf(diff, kind) {
    return diff.lines.filter((line) => line.kind === kind).map((line) => line.text).join('\n')
}

check('object keys and JSON whitespace do not count as changes', () => {
    const before = {z: 1, a: {y: 2, x: [3, {b: false, a: null}]}}
    const after = '{ "a": {"x":[3,{"a":null,"b":false}],"y":2},"z":1 }'
    const diff = same(before, after)
    assert.equal(diff.lines[1].text, '  "a": {')
    assert.ok(diff.lines.every((line, index) => line.beforeLine === index + 1 && line.afterLine === index + 1))
})

check('headers ignore name case and ordering, not value case', () => {
    const first = Object.freeze([
        Object.freeze({name: 'Content-Type', value: 'application/json'}),
        Object.freeze({name: 'X-Mode', value: 'A'}),
    ])
    const second = [{name: 'x-mode', value: 'A'}, {name: 'content-type', value: 'application/json'}]
    same(normalizeHeaders(first), normalizeHeaders(second))
    assert.equal(first[0].name, 'Content-Type')
    changed(normalizeHeaders(first), normalizeHeaders([{
        name: 'content-type',
        value: 'application/json'
    }, {name: 'x-mode', value: 'a'}]))
})

check('duplicate headers preserve every value and multiplicity', () => {
    const before = [{name: 'X-Tag', value: 'b'}, {name: 'x-tag', value: 'a'}, {name: 'X-TAG', value: 'a'}]
    const normalized = normalizeHeaders(before)
    assert.deepEqual(normalized, [{name: 'x-tag', value: 'a'}, {name: 'x-tag', value: 'a'}, {
        name: 'x-tag',
        value: 'b'
    }])
    same(normalized, normalizeHeaders([...before].reverse()))
    changed(normalized, normalizeHeaders(before.slice(1)))
    changed(normalized, normalizeHeaders(before.slice(0, 2)))
    same(normalizeHeaders([{name: '__PROTO__', value: 'safe'}, {name: 'Constructor', value: 'safe'}]),
        normalizeHeaders([{name: 'constructor', value: 'safe'}, {name: '__proto__', value: 'safe'}]))
})

check('input string becoming an array is a precise unified diff', () => {
    const before = {model: 'model', input: 'hello'}
    const after = {model: 'model', input: [{role: 'user', content: 'hello'}]}
    const diff = changed(before, after)
    assert.match(textOf(diff, 'removed'), /"input": "hello"/)
    assert.match(textOf(diff, 'added'), /"input": \[/)
    assert.match(textOf(diff, 'added'), /"role": "user"/)
    assert.match(textOf(diff, 'context'), /"model": "model"/)
})

check('stream/store additions and removed fields are highlighted', () => {
    const diff = changed({input: 'hello', legacy: 'remove', model: 'model'}, {
        input: 'hello',
        model: 'model',
        stream: true,
        store: false
    })
    assert.match(textOf(diff, 'removed'), /"legacy": "remove"/)
    assert.match(textOf(diff, 'added'), /"stream": true/)
    assert.match(textOf(diff, 'added'), /"store": false/)
    assert.match(textOf(diff, 'context'), /"input": "hello"/)
})

check('array insertion, removal, and order are significant', () => {
    let diff = changed([1, 2, 4], [1, 2, 3, 4])
    assert.equal(diff.added, 1)
    assert.equal(diff.removed, 0)
    assert.equal(textOf(diff, 'added'), '  3,')
    diff = changed([1, 2, 3, 4], [1, 2, 4])
    assert.equal(diff.added, 0)
    assert.equal(diff.removed, 1)
    changed([1, 2], [2, 1])
    same('[1,2]', [1, 2])
})

check('plain text and multiline changes have accurate line numbers', () => {
    assert.deepEqual(changed('first\nold\nlast', 'first\nnew\nlast').lines, [
        {kind: 'context', text: 'first', beforeLine: 1, afterLine: 1},
        {kind: 'removed', text: 'old', beforeLine: 2},
        {kind: 'added', text: 'new', afterLine: 2},
        {kind: 'context', text: 'last', beforeLine: 3, afterLine: 3},
    ])
    assert.equal(changed('hello', 'world').removed, 1)
    assert.equal(changed('first\nlast', 'first\nmiddle\nlast').added, 1)
    assert.equal(changed('line', 'line\n').added, 1)
    changed('line\r\nnext', 'line\nnext')
    same('  not JSON  ', '  not JSON  ')
    changed('not JSON', ' not JSON')
})

check('null, false, zero, empty string and undefined remain distinct values', () => {
    const values = [null, false, 0, '', undefined]
    for (let first = 0; first < values.length; first++) {
        for (let second = 0; second < values.length; second++) {
            if (first === second) same(values[first], values[second])
            else changed(values[first], values[second])
        }
    }
    same('null', null)
    same('false', false)
    same('0', 0)
    same('""', '')
    same('"hello"', 'hello')
    changed('"null"', null)
    changed('"false"', false)
    changed('undefined', undefined)
    changed({field: undefined}, {})
    changed({input: '{"a":1}'}, {input: {a: 1}})
})

check('dangerous JSON keys survive without prototype mutation', () => {
    const first = JSON.parse('{"__proto__":{"captureDiffPolluted":true},"constructor":{"prototype":{"safe":1}},"prototype":2}')
    const second = JSON.parse('{"prototype":2,"constructor":{"prototype":{"safe":1}},"__proto__":{"captureDiffPolluted":true}}')
    const snapshot = JSON.stringify(first)
    const diff = same(first, second)
    assert.match(textOf(diff, 'context'), /"__proto__"/)
    assert.match(textOf(diff, 'context'), /"constructor"/)
    assert.match(textOf(diff, 'context'), /"prototype"/)
    changed(first, {prototype: 2, constructor: {prototype: {safe: 1}}})
    assert.equal(Object.prototype.captureDiffPolluted, undefined)
    assert.equal(JSON.stringify(first), snapshot)
    assert.equal(Object.getPrototypeOf(first), Object.prototype)
    const noPrototype = Object.create(null)
    Object.defineProperty(noPrototype, '__proto__', {value: {x: 1}, enumerable: true})
    same(noPrototype, JSON.parse('{"__proto__":{"x":1}}'))
})

check('frozen inputs and shared references are not mutated', () => {
    const shared = Object.freeze({b: 2, a: 1})
    const first = Object.freeze({z: Object.freeze([shared, shared]), a: false})
    const second = {a: false, z: [{a: 1, b: 2}, {b: 2, a: 1}]}
    same(first, second)
    assert.deepEqual(Object.keys(shared), ['b', 'a'])
    assert.deepEqual(Object.keys(first), ['z', 'a'])
})

check('long equal prefixes never establish false equality', () => {
    for (const length of [3000, 100_000, 1_000_000]) {
        const prefix = 'x'.repeat(length)
        const diff = changed(prefix + 'BEFORE', prefix + 'AFTER', true)
        assert.match(textOf(diff, 'removed'), /BEFORE$/)
        assert.match(textOf(diff, 'added'), /AFTER$/)
        same(prefix, prefix, true)
    }
    changed({input: 'x'.repeat(100_000) + 'a'}, {input: 'x'.repeat(100_000) + 'b'}, true)
    changed(JSON.stringify({input: 'x'.repeat(100_000) + 'a'}), JSON.stringify({input: 'x'.repeat(100_000) + 'b'}), true)
})

check('large raw JSON equality is conclusive while insufficient budgets stay limited', () => {
    const firstObject = {input: 'x'.repeat(100_000), store: false}
    const secondObject = {store: false, input: firstObject.input}
    const raw = JSON.stringify(firstObject)
    const separatelyCreatedRaw = JSON.stringify({...firstObject})
    assert.equal(raw === separatelyCreatedRaw, true)
    same(raw, separatelyCreatedRaw, true)
    // These objects are semantically equal, but a bounded preview cannot prove it.
    for (const [before, after] of [[firstObject, secondObject], [raw, JSON.stringify(secondObject)]]) {
        assert.equal(equalPayloads(before, after), false)
        const preview = validate(diffPayloads(before, after))
        assert.equal(preview.equal, false)
        assert.equal(preview.limited, true)
    }
})

check('display line clipping is separate from full equality', () => {
    const prefix = 'x'.repeat(5000)
    same({input: prefix}, {input: prefix}, true)
    changed({input: prefix + 'a'}, {input: prefix + 'b'}, true)
    const hiddenFirst = 'a'.repeat(2000) + 'first' + 'b'.repeat(2000)
    const hiddenSecond = 'a'.repeat(2000) + 'other' + 'b'.repeat(2000)
    const diff = changed(hiddenFirst, hiddenSecond, true)
    assert.ok(diff.added > 0 && diff.removed > 0)
})

check('total display lines are bounded even with many short lines', () => {
    const before = Array.from({length: 4000}, (_, index) => `line ${index}`).join('\n')
    const after = before + '\nlast'
    changed(before, after, true)
    same(before, before, true)
    changed('\n'.repeat(100_000) + 'before', '\n'.repeat(100_000) + 'after', true)
})

check('LCS work is bounded and oversized output uses replacement', () => {
    const before = Array.from({length: 600}, (_, index) => `old ${index}`).join('\n')
    const after = Array.from({length: 600}, (_, index) => `new ${index}`).join('\n')
    const diff = changed(before, after, true)
    assert.equal(diff.removed, 600)
    assert.equal(diff.added, 600)
    assert.ok(diff.lines.every((line) => line.kind !== 'context'))
    const nearLimit = Array.from({length: 1000}, (_, index) => `same ${index}`).join('\n')
    const oneChange = changed(nearLimit, nearLimit.replace('same 500', 'changed 500'))
    assert.equal(oneChange.added, 1)
    assert.equal(oneChange.removed, 1)
    const manyBefore = Array.from({length: 1000}, (_, index) => `line ${index}`).join('\n')
    const manyAfter = Array.from({length: 1000}, (_, index) => `different ${index}`).join('\n')
    assert.equal(changed(manyBefore, manyAfter, true).lines.length, 1600)
})

check('deep, wide, cyclic, getter and proxy inputs do not crash or compare truncated prefixes', () => {
    function deep(leaf) {
        let value = leaf
        for (let depth = 0; depth < 100; depth++) value = {child: value}
        return value
    }

    changed(deep('a'), deep('b'), true)
    const wideA = Array.from({length: 20_000}, () => 0)
    const wideB = [...wideA]
    wideB[wideB.length - 1] = 1
    changed(wideA, wideB, true)
    const cyclicA = {}
    const cyclicB = {}
    cyclicA.self = cyclicA
    cyclicB.self = cyclicB
    changed(cyclicA, cyclicB, true)
    same(cyclicA, cyclicA, true)
    let getterCalls = 0
    const getter = Object.defineProperty({}, 'value', {
        enumerable: true, get() {
            getterCalls++;
            throw new Error('unreadable')
        }
    })
    changed(getter, {}, true)
    assert.equal(getterCalls, 0)
    const proxy = new Proxy({}, {
        ownKeys() {
            throw new Error('unreadable')
        }
    })
    changed(proxy, {}, true)
    const revocable = Proxy.revocable({}, {})
    revocable.revoke()
    changed(revocable.proxy, {}, true)
    changed(new Date(0), {}, true)
    changed(() => 1, () => 2, true)
})

check('HTML-like payloads are strings and are never executed', () => {
    globalThis.__captureDiffExecuted = false
    const html = '<script>globalThis.__captureDiffExecuted = true</script><img src=x onerror="globalThis.__captureDiffExecuted = true">'
    const diff = changed('ordinary text', html)
    assert.equal(textOf(diff, 'added'), html)
    assert.equal(globalThis.__captureDiffExecuted, false)
    const jsonDiff = changed({input: 'ordinary text'}, {input: html})
    assert.ok(jsonDiff.lines.every((line) => typeof line.text === 'string'))
    assert.equal(globalThis.__captureDiffExecuted, false)
    delete globalThis.__captureDiffExecuted
})

check('small repeated-line diffs reconstruct both sides with minimal edits', () => {
    let seed = 0x12345678

    function random() {
        seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
        return seed
    }

    function lcsLength(a, b) {
        const table = Array.from({length: a.length + 1}, () => Array(b.length + 1).fill(0))
        for (let i = 1; i <= a.length; i++) {
            for (let j = 1; j <= b.length; j++) {
                table[i][j] = a[i - 1] === b[j - 1] ? table[i - 1][j - 1] + 1 : Math.max(table[i - 1][j], table[i][j - 1])
            }
        }
        return table[a.length][b.length]
    }

    for (let iteration = 0; iteration < 250; iteration++) {
        const a = Array.from({length: random() % 12 + 1}, () => `text ${random() % 4}`)
        const b = Array.from({length: random() % 12 + 1}, () => `text ${random() % 4}`)
        const diff = validate(diffPayloads(a.join('\n'), b.join('\n')))
        assert.equal(diff.limited, false)
        assert.deepEqual(diff.lines.filter((line) => line.kind !== 'added').map((line) => line.text), a)
        assert.deepEqual(diff.lines.filter((line) => line.kind !== 'removed').map((line) => line.text), b)
        assert.equal(diff.added + diff.removed, a.length + b.length - 2 * lcsLength(a, b))
        let beforeLine = 0
        let afterLine = 0
        for (const line of diff.lines) {
            if (line.kind !== 'added') assert.equal(line.beforeLine, ++beforeLine)
            if (line.kind !== 'removed') assert.equal(line.afterLine, ++afterLine)
        }
    }
})

console.log(`Capture diff: ${checked} checks passed (including 250 randomized reconstruction cases).`)
