import {beforeRequest, expect, fixtureRule, simulationResponse, test} from './admin.fixture'
import {
    closeParameters,
    focusNode,
    inlineNodeControls,
    readCode,
    ruleEditor,
    selectCapture,
    showCanvas
} from './rules-ui.helpers'

test('从模拟样本选择数组与转义字段路径，仍可手动输入自定义路径', async ({page, admin}) => {
    admin.simulate = request => {
        const response = simulationResponse(request)
        const input = {...structuredClone(beforeRequest), metadata: {...beforeRequest.metadata, 'a/b~c': null}}
        response.results[0]!.before = input
        response.results[0]!.result.body = {
            ...input,
            model: 'fixture-new-model',
            metadata: {...input.metadata, reviewed: false}
        }
        return {body: response}
    }
    await admin.open()
    await admin.editRule()
    await focusNode(page, page.locator('.rule-graph-action').first())
    await selectCapture(page, 101)
    await page.getByTestId('simulate-capture').click()
    await expect(page.getByTestId('simulation-diff')).toBeVisible()
    const picker = inlineNodeControls(page).getByTestId('sample-path-select-path')
    const path = inlineNodeControls(page).getByLabel('字段路径', {exact: true})
    await expect(picker).toBeVisible()
    await picker.selectOption('/input/0/content')
    await expect(path).toHaveValue('/input/0/content')
    await expect(page.getByTestId('simulation-stale')).toBeVisible()
    await page.getByTestId('simulate-capture').click()
    await expect(page.getByTestId('simulation-stale')).toHaveCount(0)
    await picker.selectOption('/metadata/a~1b~0c')
    await expect(path).toHaveValue('/metadata/a~1b~0c')
    await path.fill('/custom/not-in-record')
    expect((await readCode(page)).actions[0]!.params.path).toBe('/custom/not-in-record')
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
})

test('节点坐标和缩放跨刷新保留，本地缓存只有布局不含样本或凭据', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await selectCapture(page, 101)
    await page.getByTestId('simulate-capture').click()
    await expect(page.getByTestId('simulation-diff')).toBeVisible()
    await page.getByTestId('canvas-fit').click()
    const root = page.locator('.vue-flow__node').filter({has: page.locator('.rule-graph-rule')})
    const viewport = page.getByTestId('rule-canvas').locator('.vue-flow__transformationpane')
    const before = await root.evaluate(node => (node as HTMLElement).style.transform)
    const box = await root.locator('.rule-node-drag-handle').boundingBox()
    expect(box).toBeTruthy()
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
    await page.mouse.down()
    await page.mouse.move(box!.x + box!.width / 2 + 70, box!.y + box!.height / 2 + 45, {steps: 8})
    await page.mouse.up()
    await expect.poll(() => root.evaluate(node => (node as HTMLElement).style.transform)).not.toBe(before)
    await closeParameters(page)
    const beforeZoom = await viewport.evaluate(node => (node as HTMLElement).style.transform)
    await page.getByTestId('canvas-zoom-in').click()
    await expect.poll(() => viewport.evaluate(node => (node as HTMLElement).style.transform)).not.toBe(beforeZoom)
    const position = await root.evaluate(node => (node as HTMLElement).style.transform)
    const transform = await viewport.evaluate(node => (node as HTMLElement).style.transform)
    const cache = await page.evaluate(() => Object.entries(localStorage).filter(([key]) => key.startsWith('pi-rule-layout:')))
    expect(cache.length).toBeGreaterThan(0)
    expect(JSON.stringify(cache)).not.toContain('fixture-old-model')
    expect(JSON.stringify(cache)).not.toContain('Synthetic public capture')
    expect(JSON.stringify(cache)).not.toContain('fixture-admin-token')
    expect(JSON.stringify(cache)).not.toContain('Fixture request rule')
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
    await page.reload()
    await admin.editRule()
    await expect.poll(() => root.evaluate(node => (node as HTMLElement).style.transform)).toBe(position)
    await expect.poll(() => viewport.evaluate(node => (node as HTMLElement).style.transform)).toBe(transform)
    expect(await readCode(page)).toEqual(admin.rules[0])
})

test('迁移规则的列表和画布标题随语言切换，用户改名及持久化身份原样保留', async ({page, admin}) => {
    const migrated = fixtureRule({
        id: 'migrated-rule',
        name: 'drop_environment_context',
        source: 'legacy',
        legacy_name: 'drop_environment_context',
        when: {op: 'always'},
        actions: [{
            id: 'migrated-action',
            type: 'text_replace',
            params: {
                path: '/instructions',
                match: 'regex',
                pattern: '<environment_context>.*?</environment_context>',
                replacement: '',
                dot_all: true
            }
        }],
    })
    const renamed = {...structuredClone(migrated), id: 'renamed-migrated-rule', name: 'My personal cleanup'}
    admin.rules = [migrated, renamed]
    await admin.open()
    await expect(page.locator('article h2')).toHaveText(['清理环境上下文', 'My personal cleanup'])
    await admin.editRule('清理环境上下文')
    await expect(ruleEditor(page).getByRole('heading', {name: '清理环境上下文', exact: true})).toBeVisible()
    await expect(page.locator('.rule-graph-rule .rule-graph-node-title')).toHaveText('清理环境上下文')
    await page.getByLabel('语言', {exact: true}).selectOption('en')
    await expect(page.locator('.rule-graph-rule .rule-graph-node-title')).toHaveText('Remove environment context')
    await expect(ruleEditor(page).getByRole('heading', {name: 'Remove environment context', exact: true})).toBeVisible()
    expect(await readCode(page)).toEqual(migrated)
    await showCanvas(page)
    await ruleEditor(page).getByRole('button', {name: 'Close editor', exact: true}).click()
    await expect(page.locator('article h2')).toHaveText(['Remove environment context', 'My personal cleanup'])
    await page.getByLabel('Language', {exact: true}).selectOption('zh-CN')
    await admin.editRule('My personal cleanup')
    await expect(page.locator('.rule-graph-rule .rule-graph-node-title')).toHaveText('My personal cleanup')
    expect(await readCode(page)).toEqual(renamed)
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
})
