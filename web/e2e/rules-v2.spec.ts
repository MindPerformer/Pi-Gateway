import {expect, fixtureRule, test} from './admin.fixture'
import {readCode, ruleEditor, selectCapture, showCanvas} from './rules-ui.helpers'

test('V2 默认步骤模式只提供底层动作，参数悬浮含说明与示例并随语言切换', async ({page, admin}) => {
    await admin.open()
    await admin.editRule('Fixture request rule', 'steps')
    const editor = ruleEditor(page)
    await expect(editor.getByRole('button', {name: '步骤', exact: true})).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByTestId('rule-canvas')).toHaveCount(0)
    const actions = editor.locator('#add-rule-action option')
    const types = await actions.evaluateAll(options => options.map(option => (option as HTMLOptionElement).value))
    expect(types).toEqual(expect.arrayContaining(['json_set', 'text_replace', 'if', 'for_each', 'walk', 'scope', 'call']))
    expect(types).not.toContain('sequence')
    for (const type of ['rewrite_model', 'drop_environment_context', 'drop_input_items', 'drop_tools', 'set_reasoning', 'passthrough_fields']) {
        expect(types).not.toContain(type)
    }
    const path = editor.locator('[data-rule-path="/actions/1/params/path"]')
    const help = path.getByRole('button', {name: '字段路径: 字段帮助', exact: true})
    await help.hover()
    await expect(path.getByRole('tooltip')).toBeVisible()
    await expect(path.getByRole('tooltip')).toContainText('JSON Pointer')
    await expect(path.getByRole('tooltip')).toContainText('/input/0/content/0/text')
    await page.getByLabel('语言', {exact: true}).selectOption('en')
    await expect(path.getByLabel('Field path', {exact: true})).toBeVisible()
    await expect(editor.locator('#add-rule-action')).toContainText('Set field value')
    await expect(editor.getByTestId('rule-pipeline')).not.toContainText('request_normalize')
    await path.getByRole('button', {name: 'Field path: Field help', exact: true}).hover()
    await expect(path.getByRole('tooltip')).toContainText('Examples')
    expect(await readCode(page)).toEqual(admin.rules[0])
})

test('V2 嵌套分支、遍历和计算引用在步骤、画布、代码及保存间无损往返', async ({page, admin}) => {
    const original = fixtureRule({
        actions: [{
            id: 'conditional-cleanup', type: 'if', params: {
                predicate: {op: 'exists', path: '/input'},
                then: [{
                    id: 'each-message', type: 'for_each', params: {
                        path: '/input', bind: 'message', steps: [{
                            id: 'copy-role', type: 'json_set', params: {
                                path: '/metadata/role', create_parents: true,
                                value: {
                                    $expr: {
                                        op: 'concat',
                                        args: ['role:', {$ref: {source: 'vars', path: '/message/value/role'}}]
                                    }
                                },
                            },
                        }],
                    },
                }],
                else: [{id: 'fallback', type: 'json_set', params: {path: '/input', value: [], create_parents: true}}],
            },
        }],
    })
    admin.rules[0] = structuredClone(original)
    await admin.open()
    await admin.editRule('Fixture request rule', 'steps')
    const editor = ruleEditor(page)
    for (const path of ['/actions/0/params/then', '/actions/0/params/then/0/params/steps']) {
        await editor.locator(`[data-rule-path="${path}"]`).first().getByRole('button', {name: /^展开/}).first().click()
    }
    const nestedPath = editor.locator('[data-rule-path="/actions/0/params/then/0/params/steps/0/params/path"] textarea')
    await nestedPath.fill('/metadata/copied_role')
    const expected = structuredClone(original)
    const branch = expected.actions[0]!.params.then as typeof expected.actions
    const steps = branch[0]!.params.steps as typeof expected.actions
    steps[0]!.params.path = '/metadata/copied_role'
    expect(await readCode(page)).toEqual(expected)
    await showCanvas(page)
    const branchNode = page.locator('.rule-graph-action')
    await expect(branchNode).toHaveCount(4)
    await expect(branchNode.locator('[data-rule-path="/actions/0/params/predicate"]').first()).toBeVisible()
    await expect(page.getByTestId('flow-add-then')).toBeVisible()
    await expect(page.getByTestId('flow-add-else')).toBeVisible()
    await expect(page.getByTestId('flow-add-steps')).toBeVisible()
    await expect(branchNode.locator('[data-renderer="action_array"]')).toHaveCount(0)
    await page.getByTestId('canvas-fit').click()
    const canvasPath = branchNode.locator('[data-rule-path="/actions/0/params/then/0/params/steps/0/params/path"] textarea')
    const copyID = await canvasPath.locator('xpath=ancestor::*[contains(@class,"rule-graph-node")][1]').getAttribute('data-node-id')
    await page.getByTestId('canvas-node-jump').selectOption(copyID!)
    await canvasPath.fill('/metadata/canvas_role')
    steps[0]!.params.path = '/metadata/canvas_role'
    const copyNode = page.locator(`[data-node-id="${copyID}"]`)
    await copyNode.locator('[data-value-input="/params/value"] .value-link').click()
    const calculation = page.locator('.rule-graph-value').filter({hasText: '拼接字符串'})
    await expect(calculation).toHaveCount(1)
    await expect(calculation.locator('.rule-value-input')).toHaveCount(2)
    await expect(calculation.locator('[data-rule-path="/$expr/args"]')).toHaveCount(0)
    const buttonColumns = await calculation.locator('.argument-buttons').evaluateAll(rows => rows.map(row => Array.from(row.children).map(button => button.getBoundingClientRect().left)))
    expect(buttonColumns[0]).toEqual(buttonColumns[1])
    const overflowing = await branchNode.locator('input, textarea, select').evaluateAll(controls => controls.filter(control => {
        const node = control.closest('.rule-graph-node')!.getBoundingClientRect(), r = control.getBoundingClientRect()
        return r.left < node.left - 1 || r.right > node.right + 1
    }).map(control => control.id))
    expect(overflowing).toEqual([])
    expect(await readCode(page)).toEqual(expected)
    await editor.getByRole('button', {name: '步骤', exact: true}).click()
    await selectCapture(page)
    await page.getByTestId('simulate-capture').click()
    await expect(page.getByTestId('simulation-result')).toBeVisible()
    expect(admin.simulationRequests()[0]!.rule).toEqual(expected)
    await editor.getByRole('button', {name: '保存规则', exact: true}).click()
    await expect.poll(() => admin.rules[0]!.actions).toEqual(expected.actions)
    await page.reload()
    await admin.editRule('Fixture request rule', 'steps')
    expect(await readCode(page)).toEqual({...expected, revision: 2})
})
