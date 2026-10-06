import assert from 'node:assert/strict'
import {build} from 'esbuild'
import {randomFillSync} from 'node:crypto'

const output = await build({
    entryPoints: ['src/utils/editorId.ts'],
    bundle: true,
    write: false,
    format: 'esm',
    platform: 'node'
})
const {editorId} = await import(`data:text/javascript;base64,${Buffer.from(output.outputFiles[0].text).toString('base64')}`)
const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto')
try {
    for (const value of [undefined, {}, {getRandomValues: bytes => randomFillSync(bytes)}, {
        randomUUID: () => {
            throw new Error('blocked')
        }
    }]) {
        Object.defineProperty(globalThis, 'crypto', {configurable: true, value})
        assert.equal(new Set(Array.from({length: 2000}, () => editorId())).size, 2000)
    }
    Object.defineProperty(globalThis, 'crypto', {
        configurable: true,
        value: {
            randomUUID: () => 'native-uuid', getRandomValues: () => {
                throw new Error('must not be used')
            }
        }
    })
    assert.equal(editorId(), 'native-uuid')
} finally {
    if (descriptor) Object.defineProperty(globalThis, 'crypto', descriptor); else delete globalThis.crypto
}
console.log('PASS: editor IDs without crypto/randomUUID, getRandomValues, native UUID, blocked API and 8,000 unique generated IDs')
