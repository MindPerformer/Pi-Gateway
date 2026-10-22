import assert from 'node:assert/strict'
import {readFile, readdir} from 'node:fs/promises'
import {dirname, join} from 'node:path'
import {fileURLToPath} from 'node:url'
import {parse} from '@vue/compiler-sfc'
import {parse as parseTemplate} from '@vue/compiler-dom'

const root = dirname(fileURLToPath(import.meta.url))
const templates = new Map()
const styles = new Map()
function elements(node) {
    const children = (node.children ?? []).flatMap(elements)
    return node.type === 1 ? [node, ...children] : children
}
function prop(node, name) {
    const attribute = node.props.find(item => item.type === 6 && item.name === name)
    if (attribute) { return attribute.value?.content ?? '' }
    const binding = node.props.find(item => item.type === 7 && item.name === 'bind' && item.arg?.content === name)
    return binding?.exp?.content
}
await Promise.all(['components', 'views'].map(async directory => {
    const base = join(root, 'src', directory)
    const files = (await readdir(base, {recursive: true})).filter(file => file.endsWith('.vue'))
    await Promise.all(files.map(async file => {
        const {descriptor} = parse(await readFile(join(base, file), 'utf8'))
        const nodes = elements(parseTemplate(descriptor.template.content))
        templates.set(`${directory}/${file}`, nodes)
        styles.set(`${directory}/${file}`, descriptor.styles.map(style => style.content).join('\n'))
        assert(!nodes.some(node => prop(node, 'role') === 'status'), `${file}: use native status output`)
        for (const output of nodes.filter(node => node.tag === 'output')) {
            assert(!elements(output).slice(1).some(node => ['div', 'p', 'section', 'fieldset'].includes(node.tag)), `${file}: output contains non-phrasing content`)
        }
    }))
}))

function nodes(file) {
    const result = templates.get(file)
    assert(result, file)
    return result
}
function labelledControl(file, match) {
    const all = nodes(file)
    const controls = all.filter(match)
    assert(controls.length, `${file}: target control exists`)
    for (const control of controls) {
        const id = prop(control, 'id')
        assert(id, `${file}: control has stable ID`)
        assert(all.some(node => node.tag === 'label' && prop(node, 'for') === id && node.children.length), `${file}: associated nonempty label`)
    }
}
labelledControl('components/rules/ActionEditor.vue', node => node.tag === 'select' && prop(node, 'value') === 'action.type')
labelledControl('components/rules/RuleCanvas.vue', node => node.tag === 'input' && prop(node, 'value') === 'port.name||portLabel(port)')
labelledControl('components/rules/CaptureRuleDebugger.vue', node => ['capture-search', 'capture-account', 'capture-protocol', 'capture-outcome'].includes(prop(node, 'data-testid')))
labelledControl('views/MiddlewaresView.vue', node => node.tag === 'select' && node.props.some(item => item.name === 'model' && item.exp?.content.startsWith('filters.')))
labelledControl('views/AccountsView.vue', node => node.tag === 'textarea' && node.props.some(item => item.name === 'model' && item.exp?.content === 'pasteInput'))
for (const file of ['components/Modal.vue', 'components/rules/RuleNodeSearch.vue']) {
    assert(nodes(file).some(node => node.tag === 'dialog' && prop(node, 'open') !== undefined), `${file}: native open dialog`)
}
assert(nodes('components/QuotaPanel.vue').some(node => node.tag === 'progress' && prop(node, 'max') === '100' && prop(node, 'aria-label')), 'quota exposes labelled native progress')
assert(nodes('components/BaseChart.vue').some(node => node.tag === 'figcaption'), 'chart has accessible native figure caption')
assert(nodes('components/HeadersTable.vue').some(node => node.tag === 'th' && prop(node, 'scope') === 'row'), 'headers table names each row')
const searchNodes = nodes('components/rules/RuleNodeSearch.vue')
assert(searchNodes.some(node => node.tag === 'ul' && prop(node, 'aria-label') && prop(node, 'role') === undefined), 'command results are a labelled native list without a redundant role')
const searchStyles = styles.get('components/rules/RuleNodeSearch.vue')
assert.match(searchStyles, /\.search-results\s*\{[^}]*\blist-style:\s*disc\s*;/, 'native visible markers preserve Safari list semantics')
assert.match(searchStyles, /\.search-results\s*\{[^}]*\bpadding-inline-start:\s*1\.25rem\s*;/, 'visible markers have a logical gutter rather than being clipped')
assert.match(searchStyles, /\.search-results > li::marker\s*\{\s*color:\s*var\(--color-ink-muted\);\s*\}/, 'markers remain visible instead of using empty content or transparency')
assert(searchNodes.some(node => node.tag === 'button' && prop(node, 'data-search-index') && prop(node, 'aria-labelledby') && prop(node, 'aria-describedby')), 'commands have real buttons with separate names and help')
assert(!searchNodes.some(node => ['combobox', 'listbox', 'option'].includes(prop(node, 'role'))), 'immediate commands do not claim value-selection widget roles')
assert(searchNodes.some(node => node.tag === 'output' && prop(node, 'aria-live') === 'polite'), 'search exposes a persistent polite no-results announcement')
const canvasNodes = nodes('components/rules/RuleCanvas.vue')
assert(canvasNodes.some(node => node.tag === 'button' && prop(node, 'ref') === 'addNodeButton'), 'native command button owns canvas focus restoration')
assert(canvasNodes.some(node => node.tag === 'section' && prop(node, 'data-testid') === 'rule-canvas' && prop(node, 'tabindex') === undefined), 'graph region does not create a noninteractive tab stop')
assert(nodes('components/capture/CaptureDiff.vue').some(node => node.tag === 'section' && prop(node, 'class') === 'diff-lines' && prop(node, 'aria-label') && prop(node, 'tabindex') === '0'), 'named diff scroll region remains keyboard accessible')
console.log(`PASS: ${templates.size} Vue templates, native status/dialog/progress/chart semantics and all 10 reported control label associations`)
