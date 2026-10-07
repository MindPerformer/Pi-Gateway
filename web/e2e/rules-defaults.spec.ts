import {readFileSync} from 'node:fs'
import type {Rule} from '../src/api/rules'
import {expect, test} from './admin.fixture'
import {readCode} from './rules-ui.helpers'

const defaults = [
    'legacy-block_prompt', 'legacy-drop_environment_context', 'legacy-drop_fields',
    'legacy-drop_input_items', 'legacy-drop_tools', 'legacy-passthrough_fields',
    'legacy-rewrite_model', 'legacy-set_reasoning',
    'protocol-request-normalize', 'protocol-request-finalize', 'protocol-upstream-headers',
]

for (const id of defaults) test(`实际默认规则 ${id} 的节点、折叠和展开布局`, async ({page, admin}, testInfo) => {
    const rules: Rule[] = JSON.parse(readFileSync(process.env.RULES_E2E_DEFAULTS!, 'utf8'))
    const rule = rules.find(rule => rule.id === id)!
    expect(rule, 'Use the exact expanded default installed by the backend').toBeTruthy()
    // Retain the complete real AST; a stable display name avoids locale-dependent list lookup.
    admin.rules = [{...rule, name: id, legacy_name: '', revision: 1}]
    await admin.open()
    await admin.editRule(id)
    const nodes = page.locator('.rule-graph-node')

    function countActions(actions: Rule['actions']): number {
        return actions.reduce((count, action) => count + (action.type === 'sequence' ? 0 : 1) + ['steps', 'then', 'else'].reduce((children, key) => children + (Array.isArray(action.params[key]) ? countActions(action.params[key] as Rule['actions']) : 0), 0) + (action.type === 'scope' ? Object.values(action.params.functions as Record<string, Rule['actions']>).reduce((sum, list) => sum + countActions(list), 0) : 0), 0)
    }

    await expect(page.locator('.rule-graph-action')).toHaveCount(countActions(rule.actions))
    await expect(page.locator('.rule-graph-value')).toHaveCount(0)
    // Empty legacy defaults have no action; every populated workflow must show a real entry edge.
    if (countActions(rule.actions) > 0) {
        const rootID = await page.locator('.rule-graph-rule').getAttribute('data-node-id')
        await expect(page.locator('.vue-flow__edge').filter({has: page.locator('path.vue-flow__edge-path')})).not.toHaveCount(0)
        await expect.poll(() => page.locator('.vue-flow__edge').evaluateAll((edges, id) => edges.some(edge => edge.getAttribute('aria-label')?.startsWith(`Edge from ${id} to `)), rootID)).toBe(true)
    }
    await expect(nodes.locator('[data-renderer="action_array"]')).toHaveCount(0)
    await expect(page.locator('form.rule-editor [role="alert"]')).toHaveCount(0)

    async function inspectLayout() {
        await expect.poll(async () => nodes.evaluateAll(nodes => {
            const rects = nodes.map(node => node.getBoundingClientRect())
            return rects.some((r, i) => rects.slice(i + 1).some(other => r.left < other.right - .5 && r.right > other.left + .5 && r.top < other.bottom - .5 && r.bottom > other.top + .5))
        })).toBe(false)
        const bad = await nodes.evaluateAll(nodes => nodes.flatMap(node => {
            const parent = node.getBoundingClientRect()
            return Array.from(node.querySelectorAll('input, textarea, select, .rule-field-heading label')).filter(control => {
                const r = control.getBoundingClientRect()
                return r.width > 0 && r.height > 0 && (r.left < parent.left - 1 || r.right > parent.right + 1 || getComputedStyle(control).writingMode !== 'horizontal-tb')
            }).map(control => control.getAttribute('id') ?? control.textContent)
        }))
        expect(bad, 'Visible parameter controls stay inside their node and read horizontally').toEqual([])
    }

    await inspectLayout()
    const valueEntry = page.locator('.rule-graph-action [data-value-input] .value-link').first()
    if (await valueEntry.count()) {
        const id = await valueEntry.locator('xpath=ancestor::*[contains(@class,"rule-graph-node")][1]').getAttribute('data-node-id')
        await page.getByTestId('canvas-node-jump').selectOption(id!)
        await valueEntry.click()
        await expect(page.locator('.rule-graph-value')).not.toHaveCount(0)
        await expect(page.locator('.rule-graph-value [data-rule-path="/$expr/args"]')).toHaveCount(0)
        await inspectLayout()
    }
    if (rule.actions.length > 8) {
        const zoom = await page.locator('.vue-flow__transformationpane').evaluate(element => Number((element as HTMLElement).style.transform.match(/scale\(([^)]+)\)/)?.[1]))
        expect(zoom, 'Long default pipelines open at a readable entry point').toBeGreaterThan(.4)
    }
    await page.getByTestId('canvas-fit').click()
    // Unfold one long expression/branch, then unfold its nested parameters as users do.
    const longFields = page.locator('.rule-graph-action > .rule-node-controls > .rule-node-fields > .rule-node-parameter-body > .is-folded')
    if (await longFields.count()) {
        const nodeID = await longFields.first().locator('xpath=ancestor::*[contains(@class,"rule-graph-node")][1]').getAttribute('data-node-id')
        await page.getByTestId('canvas-node-jump').selectOption(nodeID!)
        await longFields.first().locator(':scope > .rule-field-heading > .rule-field-fold').click()
        // Exercise actual nested branches/expressions, not only the outer summary.
        const nested = page.locator('.rule-graph-action .rule-field-control .is-folded > .rule-field-heading > .rule-field-fold').filter({visible: true})
        for (let depth = 0; depth < 2 && await nested.count(); depth++) await nested.first().click()
        await inspectLayout()
    }
    expect(await nodes.evaluateAll(nodes => Math.max(...nodes.map(node => (node as HTMLElement).offsetHeight))), 'Expanded default nodes remain bounded and scroll their parameters').toBeLessThan(760)
    await page.locator('.rule-canvas-toolbar').getByRole('button', {name: '自动整理', exact: true}).click()
    await inspectLayout()
    await page.getByTestId('canvas-fit').click()
    await page.screenshot({path: testInfo.outputPath(`${id}.png`)})
    // Parameter folding and layout changes must never rewrite the installed rule.
    const code = await readCode(page)
    expect(code.actions).toEqual(rule.actions)
    expect(code.when).toEqual(rule.when)
})
