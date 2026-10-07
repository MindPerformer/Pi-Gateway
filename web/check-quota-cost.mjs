import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import * as vue from 'vue'
import {compileScript, parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import {build} from 'esbuild'
import {fileURLToPath} from 'node:url'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')

function load(source, imports) {
    const compiled = ts.transpileModule(source, {
        compilerOptions: {
            module: ts.ModuleKind.CommonJS,
            target: ts.ScriptTarget.ES2022
        }
    })
    const exports = {}
    new Function('exports', 'require', compiled.outputText)(exports, name => {
        assert.ok(Object.hasOwn(imports, name), `Unexpected import: ${name}`)
        return imports[name]
    })
    return exports
}

const bundle = await build({
    entryPoints: [fileURLToPath(new URL('./src/i18n/messages.ts', import.meta.url))],
    bundle: true,
    write: false,
    platform: 'node',
    format: 'esm',
    logLevel: 'silent'
})
const messages = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`)
let locale = 'en'
const t = (key, params) => messages.translate(locale, key, params)
const i18n = {
    translateNow: t,
    currentLocale: () => locale,
    useI18n: () => ({t}),
    formatDateTime: value => `time:${value}`
}
const ui = load(read('./src/stores/ui.ts'), {
    pinia: {
        defineStore: () => {
        }
    }, vue, '../i18n': i18n
})
const money = load(read('./src/api/stats-format.ts'), {'../i18n': i18n})
const {descriptor} = parse(read('./src/components/QuotaCostSummary.vue'))
const compiled = compileScript(descriptor, {id: 'quota-cost-check', inlineTemplate: true}).content
const component = load(compiled, {vue, '../api/stats-format': money, '../i18n': i18n, '../stores/ui': ui}).default
const render = props => renderToString(vue.createSSRApp({render: () => vue.h(component, props)}))
const usage = {cost_micros: 10000000, priced_requests: 2, unpriced_requests: 0}
const windowCost = {
    limit_id: 'codex',
    role: 'secondary',
    start_at: 1,
    as_of: 200,
    usage,
    estimated_total_usd: 40,
    estimated_remaining_usd: 30
}

for (const lang of ['en', 'zh-CN']) {
    locale = lang
    assert.equal(ui.formatUntil(3 * 86400000 + 7 * 3600000), '3d 7h')
    assert.equal(ui.formatUntil(2 * 86400000), '2d 0h')
    assert.equal(ui.formatUntil(86400000), '1d 0h')
    assert.equal(ui.formatUntil(3600000 + 2 * 60000), '1h 2m')
    assert.equal(ui.formatUntil(1), '1m')
    assert.equal(ui.formatUntil(0), t('common.expired'))
    assert.equal(ui.formatUntil(-1), t('common.expired'))
    assert.equal(ui.formatUntil(NaN), '—')
    const html = await render({usage, windowCost})
    for (const amount of [10, 40, 30]) assert.ok(html.includes(money.money(amount)), html)
    for (const key of ['quota.windowCost', 'quota.estimatedTotal', 'quota.estimatedRemaining', 'quota.costHint', 'quota.costEstimateHint']) assert.ok(html.includes(t(key)), key)
    assert.ok(html.includes('time:200'))
    const partial = {...usage, cost_micros: 0, priced_requests: 0, unpriced_requests: 2}
    const unknown = await render({
        usage: partial,
        windowCost: {
            ...windowCost,
            usage: partial,
            estimated_total_usd: null,
            estimated_remaining_usd: null,
            unavailable_reason: 'unpriced'
        }
    })
    assert.ok(unknown.includes('—'))
    assert.ok(!unknown.includes(money.money(0)))
    assert.ok(unknown.includes(t('quota.costUnpriced')))
    assert.ok(unknown.includes(t('quota.unpricedRequests', {count: 2})))
    const zero = await render({usage: {cost_micros: 0, priced_requests: 1, unpriced_requests: 0}})
    assert.ok(zero.includes(money.money(0)))
    const compact = await render({usage: partial, compact: true, cycleLabel: t('quota.window7d')})
    assert.ok(compact.includes(t('quota.partialCost')))
    assert.ok(compact.includes(t('quota.window7d')))
}
console.log('PASS: quota USD cost/estimate rendering in both languages, partial/missing/zero costs and day/hour countdown boundaries')
