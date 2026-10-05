import assert from 'node:assert/strict'
import Module from 'node:module'
import {build} from 'esbuild'
import {dirname, join} from 'node:path'
import {fileURLToPath} from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))

export async function checkRuleApi() {
    globalThis.document ??= {documentElement: {lang: 'en'}}
    globalThis.localStorage ??= {
        getItem: () => 'en', setItem() {
        }, removeItem() {
        }
    }
    const bundle = await build({
        entryPoints: [join(root, 'src/api/client.ts')],
        bundle: true,
        write: false,
        format: 'cjs',
        platform: 'node',
        external: ['vue'],
        logLevel: 'silent'
    })
    const module = new Module(join(root, 'virtual-rule-api.cjs'));
    module.filename = module.id;
    module.paths = Module._nodeModulePaths(root);
    module._compile(bundle.outputFiles[0].text, module.filename)
    const {api, ApiError} = module.exports
    const calls = []
    const original = globalThis.fetch
    let response = {rules: [], total: 60, page: 3, page_size: 20, version: 9}
    let status = 200
    globalThis.fetch = async (path, options = {}) => {
        calls.push({path, options});
        return new Response(JSON.stringify(response), {status})
    }
    try {
        const listed = await api.listRules({
            search: 'quoted & text',
            phase: 'request',
            enabled: false,
            limit: 20,
            offset: 40
        })
        const url = new URL(calls.at(-1).path, 'http://localhost')
        assert.equal(url.searchParams.get('page'), '3');
        assert.equal(url.searchParams.get('page_size'), '20');
        assert.equal(url.searchParams.get('enabled'), 'false');
        assert.equal(url.searchParams.get('search'), 'quoted & text');
        assert.equal(listed.offset, 40);
        assert.equal(listed.limit, 20)
        await api.moveRule('rule on another page', 7, 'down', 9)
        assert.equal(calls.at(-1).path, '/api/rules/reorder')
        assert.deepEqual(JSON.parse(calls.at(-1).options.body), {
            id: 'rule on another page',
            expected_revision: 7,
            direction: 'down',
            expected_version: 9
        })
        assert.equal(calls.at(-1).options.method, 'POST')
        await api.batchRules([{id: 'a', expected_revision: 2}, {id: 'b', expected_revision: 4}], false, 9)
        assert.deepEqual(JSON.parse(calls.at(-1).options.body), {
            items: [{id: 'a', expected_revision: 2}, {
                id: 'b',
                expected_revision: 4
            }], enabled: false, expected_version: 9
        })
        const rule = {schema_version: 1, name: 'draft', phase: 'request', when: {op: 'always'}, actions: []}
        await api.createRule(rule, 9);
        assert.deepEqual(JSON.parse(calls.at(-1).options.body), {rule, expected_version: 9})
        await api.updateRule('id/with slash', rule, 3);
        assert.equal(calls.at(-1).path, '/api/rules/id%2Fwith%20slash');
        assert.deepEqual(JSON.parse(calls.at(-1).options.body), {rule, expected_revision: 3})
        await api.deleteRule('a', 3);
        assert.equal(calls.at(-1).path, '/api/rules/a?expected_revision=3');
        assert.equal(calls.at(-1).options.method, 'DELETE')
        await api.duplicateRule('a', 3);
        assert.equal(calls.at(-1).path, '/api/rules/a/duplicate');
        assert.equal(JSON.parse(calls.at(-1).options.body).expected_revision, 3)
        const input = {body: {input: [null, false]}, client_body: {original: true}, context: {model: 'sample'}}
        await api.simulateRules({rule, phase: 'request', input});
        assert.equal(calls.at(-1).path, '/api/rules/simulate');
        assert.deepEqual(JSON.parse(calls.at(-1).options.body), {rule, phase: 'request', input})
        await api.updateMiddleware('drop_tools', {config: '{}', expected_revision: 3});
        assert.equal(JSON.parse(calls.at(-1).options.body).expected_revision, 3)
        await api.clearCaptures(undefined, true);
        assert.equal(calls.at(-1).path, '/api/captures?unassigned=true')
        status = 409;
        response = {error: 'revision conflict', errors: [{path: '/name', message: 'conflict'}]}
        await assert.rejects(() => api.updateRule('a', rule, 2), error => error instanceof ApiError && error.status === 409 && error.payload.errors[0].path === '/name')
    } finally {
        globalThis.fetch = original
    }
    console.log('PASS: rules API page/filter conversion, cross-page true-neighbour endpoint, revision/version CAS, CRUD, simulation, legacy repair and conflict payloads')
}
