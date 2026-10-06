import type {Locator, Page} from '@playwright/test'
import {expect, fixtureRule, test} from './admin.fixture'
import {
    inlineNodeControls,
    nodeParameters,
    nodeSearch,
    openAdvanced,
    readCode,
    ruleEditor,
    ruleName,
    runSimulation,
    selectCapture,
    selectedNode,
    showCanvas
} from './rules-ui.helpers'

const canvas = (page: Page) => page.getByTestId('rule-canvas')
const viewport = (page: Page) => canvas(page).locator('.vue-flow__transformationpane')
const graphNodes = (page: Page) => page.locator('.rule-graph-node')
const actionNode = (page: Page, title: string) => page.locator('.rule-graph-action').filter({has: page.locator('.rule-graph-node-title', {hasText: title})})
const conditionNode = (page: Page, title: string) => page.locator('.rule-graph-condition').filter({has: page.locator('.rule-graph-node-title').filter({hasText: new RegExp(`^${title}$`)})})

async function canvasBackground(page: Page) {
    await canvas(page).scrollIntoViewIfNeeded()
    const point = await canvas(page).evaluate(element => {
        const rect = element.getBoundingClientRect()
        const top = Math.max(rect.top + 20, 20), bottom = Math.min(rect.bottom - 20, innerHeight - 20)
        for (const fy of [.75, .5, .25, .9, .1]) {
            for (const fx of [.65, .4, .8, .2, .9, .1]) {
                // Use integer CSS pixels so MouseEvent.clientX/Y match the requested coordinates.
                const x = Math.round(rect.left + rect.width * fx), y = Math.round(top + (bottom - top) * fy)
                const target = document.elementFromPoint(x, y)
                if (target && element.contains(target) && !target.closest('.vue-flow__node, .vue-flow__edge, .vue-flow__handle, button, input, textarea, select')) return {
                    x,
                    y
                }
            }
        }
        return null
    })
    expect(point, 'Find real unobstructed canvas background, rather than dispatching a synthetic pane event').toBeTruthy()
    return point!
}

async function searchFor(page: Page, query: string) {
    await expect(nodeSearch(page)).toBeVisible()
    const input = page.getByTestId('node-search-input')
    await expect(input).toBeFocused()
    await input.fill(query)
    return input
}

async function chooseModule(page: Page, kind: 'condition' | 'action', type: string) {
    await searchFor(page, type)
    await page.getByTestId(`node-search-${kind}-${type}`).click()
    await expect(nodeSearch(page)).toHaveCount(0)
    await expect(selectedNode(page)).toHaveCount(1)
    await expect(nodeParameters(page)).not.toBeVisible()
}

async function frameSettled(page: Page) {
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

function containsCondition(value: unknown, match: (condition: Record<string, unknown>) => boolean): boolean {
    if (!value || typeof value !== 'object') return false
    const condition = value as Record<string, unknown>
    if (match(condition)) return true
    return Array.isArray(condition.conditions) && condition.conditions.some(child => containsCondition(child, match))
}

async function nodePositions(page: Page) {
    return page.locator('.vue-flow__node').evaluateAll(nodes => nodes.map(node => ({
        id: node.getAttribute('data-id'),
        transform: (node as HTMLElement).style.transform
    })))
}

async function inputDrag(page: Page, control: Locator) {
    const bounds = await control.boundingBox()
    expect(bounds).toBeTruthy()
    await page.mouse.move(bounds!.x + 8, bounds!.y + bounds!.height / 2)
    await page.mouse.down()
    await page.mouse.move(bounds!.x + Math.min(bounds!.width - 8, 75), bounds!.y + bounds!.height / 2, {steps: 8})
    await page.mouse.up()
}

test('节点内直接编辑条件、动作和规则，输入拖选、滚轮、Delete 与 Enter 不误操作画布', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await expect(nodeParameters(page)).not.toBeVisible()
    await page.getByTestId('canvas-fit').click()
    const modelNode = actionNode(page, '修改模型')
    await modelNode.locator('.rule-node-drag-handle').click()
    await expect(nodeParameters(page)).not.toBeVisible()
    const originalPositions = await nodePositions(page)
    const originalTransform = await viewport(page).getAttribute('style')
    const model = inlineNodeControls(page, modelNode).getByLabel('当前模型', {exact: true})
    await model.fill('Typing stays inside the node')
    await inputDrag(page, model)
    await model.press('Control+a')
    await model.press('Delete')
    await expect(model).toHaveValue('')
    await expect(graphNodes(page)).toHaveCount(6)
    await model.fill('fixture-inline-model')
    await model.press('Enter')
    await expect(ruleEditor(page)).toBeVisible()
    await model.fill('fixture-inline-model')
    await model.press('Control+d')
    await expect(graphNodes(page)).toHaveCount(6)
    await model.hover()
    await page.mouse.wheel(0, 280)
    await frameSettled(page)
    expect(await viewport(page).getAttribute('style')).toBe(originalTransform)
    expect(await nodePositions(page)).toEqual(originalPositions)
    const condition = inlineNodeControls(page, conditionNode(page, '字段存在'))
    await condition.getByLabel('数据来源', {exact: true}).selectOption('client')
    await condition.getByLabel('字段路径', {exact: true}).fill('/metadata/count')
    const root = inlineNodeControls(page, page.locator('.rule-graph-rule'))
    await root.getByLabel('优先级', {exact: true}).fill('17')
    await root.getByLabel('优先级', {exact: true}).press('Enter')
    await expect(nodeParameters(page)).not.toBeVisible()
    expect(admin.requests.filter(request => ['POST', 'PUT', 'DELETE'].includes(request.method))).toEqual([])
    const updated = await readCode(page)
    expect(updated).toMatchObject({
        id: 'rule-request',
        priority: 17,
        when: {conditions: [{op: 'eq'}, {op: 'exists', source: 'client', path: '/metadata/count'}]}
    })
    expect(updated.actions[0]).toEqual(admin.rules[0]!.actions[0])
    expect(updated.actions[1]).toEqual({...admin.rules[0]!.actions[1], params: {model: 'fixture-inline-model'}})
})

test('节点编辑同步 JSON 并使模拟过期，撤销重做保留原动作身份和未设置字段', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await selectCapture(page)
    await runSimulation(page)
    await page.getByTestId('canvas-fit').click()
    const model = inlineNodeControls(page, actionNode(page, '修改模型')).getByLabel('当前模型', {exact: true})
    await model.fill('fixture-inline-history')
    await expect(page.getByTestId('simulation-stale')).toBeVisible()
    await expect(page.locator('.rule-graph-node-status')).toHaveCount(0)
    await canvas(page).focus()
    await page.keyboard.press('Control+z')
    await expect(model).toHaveValue('fixture-new-model')
    await page.keyboard.press('Control+Shift+z')
    await expect(model).toHaveValue('fixture-inline-history')
    await runSimulation(page)
    expect(admin.simulationRequests().at(-1)!.rule.actions[1]).toEqual({
        id: 'action-model',
        type: 'rewrite_model',
        params: {model: 'fixture-inline-history'}
    })
    const updated = await readCode(page)
    expect(updated.actions[1]).toEqual({
        id: 'action-model',
        type: 'rewrite_model',
        params: {model: 'fixture-inline-history'}
    })
    expect(updated.actions[0]).toEqual(admin.rules[0]!.actions[0])
    expect(updated.when).toEqual(admin.rules[0]!.when)
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
})

test('双击空白打开搜索，上下键选中结果、Enter 创建、Escape 关闭且不缩放或提交', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await page.getByTestId('canvas-fit').click()
    const point = await canvasBackground(page)
    const before = await viewport(page).getAttribute('style')
    await page.mouse.dblclick(point.x, point.y)
    const input = await searchFor(page, 'exists')
    expect(await viewport(page).getAttribute('style')).toBe(before)
    await expect(nodeSearch(page).getByRole('option')).toHaveCount(2)
    const first = page.getByTestId('node-search-condition-exists')
    const second = page.getByTestId('node-search-condition-not_exists')
    await expect(first).toHaveAttribute('aria-selected', 'true')
    await input.press('ArrowDown')
    await expect(second).toHaveAttribute('aria-selected', 'true')
    await input.press('ArrowUp')
    await expect(first).toHaveAttribute('aria-selected', 'true')
    await input.press('ArrowDown')
    await input.press('Enter')
    await expect(nodeSearch(page)).toHaveCount(0)
    await expect(selectedNode(page).locator('.rule-graph-node-title')).toHaveText('字段不存在')
    await inlineNodeControls(page).getByLabel('字段路径', {exact: true}).fill('/missing')
    const count = await graphNodes(page).count()
    await page.getByTestId('canvas-add-node').click()
    await searchFor(page, 'rewrite_model')
    await page.keyboard.press('Escape')
    await expect(nodeSearch(page)).toHaveCount(0)
    await expect(graphNodes(page)).toHaveCount(count)
    await expect(canvas(page)).toBeFocused()
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
    const updated = await readCode(page)
    expect(containsCondition(updated.when, condition => condition.op === 'not_exists' && condition.path === '/missing')).toBe(true)
    expect(containsCondition(updated.when, condition => condition.op === 'eq' && condition.path === '/model')).toBe(true)
    expect(containsCondition(updated.when, condition => condition.op === 'exists' && condition.path === '/input')).toBe(true)
    expect(updated.actions).toEqual(admin.rules[0]!.actions)
})

test('节点追加条件、工具栏在选中动作后插入、节点快捷继续插入，保存顺序和原 ID 不变', async ({page, admin}) => {
    const original = structuredClone(admin.rules[0]!)
    await admin.open()
    await admin.editRule()
    await page.getByTestId('canvas-fit').click()
    const existingNodeIDs = await graphNodes(page).evaluateAll(nodes => nodes.map(node => node.getAttribute('data-node-id')))
    await conditionNode(page, '任一满足').getByTestId('node-add-condition').click()
    await expect(page.getByTestId('node-search-context')).toContainText('任一满足')
    await expect(nodeSearch(page).locator('[data-testid^="node-search-action-"]')).toHaveCount(0)
    await chooseModule(page, 'condition', 'exists')
    await inlineNodeControls(page).getByLabel('字段路径', {exact: true}).fill('/metadata/reviewed')
    await page.getByTestId('canvas-fit').click()
    await actionNode(page, '设置字段值').locator('.rule-node-drag-handle').click()
    await page.getByTestId('canvas-add-node').click()
    await expect(page.getByTestId('node-search-context')).toContainText('设置字段值')
    await chooseModule(page, 'action', 'rewrite_model')
    await inlineNodeControls(page).getByLabel('当前模型', {exact: true}).fill('fixture-inserted-first')
    await selectedNode(page).getByTestId('node-add-action').click()
    await expect(page.getByTestId('node-search-context')).toContainText('修改模型')
    await expect(nodeSearch(page).locator('[data-testid^="node-search-condition-"]')).toHaveCount(0)
    await chooseModule(page, 'action', 'rewrite_model')
    await inlineNodeControls(page).getByLabel('当前模型', {exact: true}).fill('fixture-inserted-second')
    const currentIDs = await graphNodes(page).evaluateAll(nodes => nodes.map(node => node.getAttribute('data-node-id')))
    expect(currentIDs).toEqual(expect.arrayContaining(existingNodeIDs))
    const updated = await readCode(page)
    expect(updated.when.op).toBe('any')
    expect(updated.when.conditions?.slice(0, 2)).toEqual(original.when.conditions)
    expect(updated.when.conditions?.[2]).toMatchObject({op: 'exists', path: '/metadata/reviewed'})
    expect(updated.actions).toHaveLength(4)
    expect(updated.actions[0]).toEqual(original.actions[0])
    expect(updated.actions[3]).toEqual(original.actions[1])
    expect(updated.actions.slice(1, 3).map(action => action.params.model)).toEqual(['fixture-inserted-first', 'fixture-inserted-second'])
    expect(new Set(updated.actions.map(action => action.id)).size).toBe(4)
    await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
    await expect.poll(() => admin.rules[0]!.actions).toEqual(updated.actions)
    expect(admin.rules[0]!.when).toEqual(updated.when)
    await page.reload()
    await admin.editRule()
    expect((await readCode(page)).actions).toEqual(updated.actions)
})

test('从动作输出端口拖到空白只搜索动作，取消不新增，确认后自动连线且保留原 ID 和顺序', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await page.getByTestId('canvas-fit').click()
    const originalIDs = await graphNodes(page).evaluateAll(nodes => nodes.map(node => node.getAttribute('data-node-id')))
    const source = actionNode(page, '修改模型').locator('[data-handleid="action-out"]')
    const dragToBackground = async () => {
        const point = await canvasBackground(page)
        await expect(source).toBeInViewport()
        const bounds = await source.boundingBox()
        expect(bounds).toBeTruthy()
        await page.mouse.move(bounds!.x + bounds!.width / 2, bounds!.y + bounds!.height / 2)
        await page.mouse.down()
        await page.mouse.move(point.x, point.y, {steps: 12})
        await page.mouse.up()
        await expect(nodeSearch(page)).toBeVisible()
        await expect(page.getByTestId('node-search-context')).toContainText('修改模型')
        await expect(nodeSearch(page).locator('[data-testid^="node-search-condition-"]')).toHaveCount(0)
        await expect(page.getByTestId('node-search-action-rewrite_model')).toBeVisible()
    }
    await dragToBackground()
    await page.keyboard.press('Escape')
    await expect(nodeSearch(page)).toHaveCount(0)
    await expect(graphNodes(page)).toHaveCount(originalIDs.length)
    await expect(page.locator('.vue-flow__edge')).toHaveCount(5)
    expect(await graphNodes(page).evaluateAll(nodes => nodes.map(node => node.getAttribute('data-node-id')))).toEqual(originalIDs)
    await dragToBackground()
    await chooseModule(page, 'action', 'rewrite_model')
    await inlineNodeControls(page).getByLabel('当前模型', {exact: true}).fill('fixture-drag-created')
    await expect(page.locator('.rule-graph-action')).toHaveCount(3)
    await expect(page.locator('.vue-flow__edge')).toHaveCount(6)
    expect(await graphNodes(page).evaluateAll(nodes => nodes.map(node => node.getAttribute('data-node-id')))).toEqual(expect.arrayContaining(originalIDs))
    const updated = await readCode(page)
    expect(updated.actions.slice(0, 2)).toEqual(admin.rules[0]!.actions)
    expect(updated.actions[2]).toMatchObject({type: 'rewrite_model', params: {model: 'fixture-drag-created'}})
    expect(new Set(updated.actions.map(action => action.id)).size).toBe(3)
    await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
    await expect.poll(() => admin.rules[0]!.actions).toEqual(updated.actions)
})

test('平移缩放后双击空白按真实画布坐标放置新节点，无选择时动作追加在末尾', async ({page, admin}) => {
    await admin.open()
    await admin.editRule()
    await page.getByTestId('canvas-fit').click()
    const beforeZoom = await viewport(page).getAttribute('style')
    await page.getByTestId('canvas-zoom-in').click()
    await expect.poll(() => viewport(page).getAttribute('style')).not.toBe(beforeZoom)
    const beforePan = await viewport(page).getAttribute('style')
    const pan = await canvasBackground(page)
    await page.mouse.move(pan.x, pan.y)
    await page.mouse.down()
    await page.mouse.move(pan.x - 75, pan.y - 45, {steps: 8})
    await page.mouse.up()
    await expect.poll(() => viewport(page).getAttribute('style')).not.toBe(beforePan)
    const point = await canvasBackground(page)
    const expected = await canvas(page).evaluate((element, screen) => {
        const rect = element.getBoundingClientRect()
        const pane = element.querySelector('.vue-flow__transformationpane')!
        const matrix = new DOMMatrix(getComputedStyle(pane).transform)
        return {x: (screen.x - rect.left - matrix.e) / matrix.a, y: (screen.y - rect.top - matrix.f) / matrix.d}
    }, point)
    await page.mouse.dblclick(point.x, point.y)
    await chooseModule(page, 'action', 'rewrite_model')
    const position = await selectedNode(page).evaluate(node => {
        const matrix = new DOMMatrix(getComputedStyle(node.closest('.vue-flow__node')!).transform)
        return {x: matrix.e, y: matrix.f}
    })
    expect(position.x).toBeCloseTo(expected.x, 1)
    expect(position.y).toBeCloseTo(expected.y, 1)
    await inlineNodeControls(page).getByLabel('当前模型', {exact: true}).fill('fixture-at-click-position')
    const updated = await readCode(page)
    expect(updated.actions.slice(0, 2)).toEqual(admin.rules[0]!.actions)
    expect(updated.actions[2]!.params.model).toBe('fixture-at-click-position')
})

test('右键与 Space 可搜索，支持双语检索和阶段过滤，明暗浮层及节点背景清晰', async ({page, admin}, testInfo) => {
    await admin.open()
    await admin.editRule()
    await page.getByTestId('canvas-fit').click()
    const equals = conditionNode(page, '等于')
    const exists = conditionNode(page, '字段存在')
    await expect(equals).toBeVisible()
    await expect(exists).toBeVisible()
    const equalsBounds = await equals.boundingBox()
    const existsBounds = await exists.boundingBox()
    expect(equalsBounds).toBeTruthy()
    expect(existsBounds).toBeTruthy()
    expect(equalsBounds!.height).toBeGreaterThan(0)
    expect(existsBounds!.height).toBeGreaterThan(0)
    // Check the initial layout, before search or manual auto-arrange can hide overlap.
    expect(equalsBounds!.y + equalsBounds!.height).toBeLessThanOrEqual(existsBounds!.y)
    const point = await canvasBackground(page)
    await page.mouse.click(point.x, point.y, {button: 'right'})
    await searchFor(page, '修改模型')
    await expect(nodeSearch(page)).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    const lightPopup = await nodeSearch(page).evaluate(element => getComputedStyle(element).backgroundColor)
    const lightNode = await page.locator('.rule-graph-rule').evaluate(element => getComputedStyle(element).backgroundColor)
    await expect(page.getByTestId('node-search-action-rewrite_model')).toBeVisible()
    await searchFor(page, 'Change model')
    await expect(page.getByTestId('node-search-action-rewrite_model')).toBeVisible()
    await searchFor(page, 'drop_event')
    await expect(nodeSearch(page).getByRole('option')).toHaveCount(0)
    await page.keyboard.press('Escape')
    await canvas(page).focus()
    await page.keyboard.press('Space')
    await searchFor(page, 'rewrite_model')
    await expect(page.getByTestId('node-search-action-rewrite_model')).toBeVisible()
    await page.keyboard.press('Escape')
    await page.locator('.sidebar-controls button').first().click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.getByTestId('canvas-add-node').click()
    await searchFor(page, 'rewrite_model')
    await expect(nodeSearch(page)).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    await expect.poll(() => nodeSearch(page).evaluate(element => getComputedStyle(element).backgroundColor)).not.toBe(lightPopup)
    await expect.poll(() => page.locator('.rule-graph-rule').evaluate(element => getComputedStyle(element).backgroundColor)).not.toBe(lightNode)
    const darkScreenshot = testInfo.outputPath('rules-node-search-dark.png')
    await page.screenshot({path: darkScreenshot})
    await testInfo.attach('深色节点快捷搜索与画布', {path: darkScreenshot, contentType: 'image/png'})
    await page.keyboard.press('Escape')
    await ruleEditor(page).getByRole('button', {name: '关闭编辑器', exact: true}).click()
    await admin.editRule('Fixture event rule')
    await page.getByTestId('canvas-add-node').click()
    await searchFor(page, 'drop_event')
    await expect(page.getByTestId('node-search-action-drop_event')).toBeVisible()
    await page.keyboard.press('Escape')
    expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
})

test('高级侧栏仍可编辑嵌套谓词，复杂 JSON、false、零、null 和引用无损保存', async ({page, admin}) => {
    const original = fixtureRule()
    original.actions[0]!.params.value = {
        flag: false,
        count: 0,
        nullable: null,
        escaped: {$literal: {$ref: {source: 'current', path: '/model'}}}
    }
    original.actions.push({
        id: 'action-filter', type: 'array_filter', params: {
            path: '/input', mode: 'remove_matches', predicate: {
                op: 'all', conditions: [
                    {
                        op: 'eq',
                        source: 'item',
                        path: '/role',
                        value: {$ref: {source: 'context', path: '/role', encoding: 'value'}}
                    },
                    {op: 'exists', source: 'item', path: '/content'},
                ]
            }
        }
    })
    admin.rules[0] = structuredClone(original)
    await admin.open()
    await admin.editRule()
    await expect(nodeParameters(page)).not.toBeVisible()
    await page.getByTestId('canvas-fit').click()
    const filter = actionNode(page, '筛选数组')
    await expect(filter.getByTestId('rule-node-predicate-summary')).toContainText('全部满足')
    await openAdvanced(page, filter)
    await nodeParameters(page).locator('[data-rule-path="/actions/2/params/predicate/conditions/1/path"] textarea').fill('/content/text')
    const updated = await readCode(page)
    const expected = structuredClone(original)
    const predicate = expected.actions[2]!.params.predicate as { conditions: { path: string }[] }
    predicate.conditions[1]!.path = '/content/text'
    expect(updated).toEqual(expected)
    await showCanvas(page)
    await expect(nodeParameters(page)).not.toBeVisible()
    await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
    await expect.poll(() => admin.rules[0]!.actions).toEqual(expected.actions)
})

test('窄屏新增按钮与搜索浮层不溢出，可直接节点编辑后保存', async ({page, admin}, testInfo) => {
    await page.setViewportSize({width: 390, height: 844})
    await admin.open()
    await page.getByRole('button', {name: '新增规则', exact: true}).click()
    await ruleName(page).fill('Comfy narrow draft')
    await expect(nodeParameters(page)).not.toBeVisible()
    await page.getByTestId('canvas-add-node').click()
    await searchFor(page, 'rewrite_model')
    await expect(nodeSearch(page)).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    const bounds = await nodeSearch(page).boundingBox()
    expect(bounds!.x).toBeGreaterThanOrEqual(0)
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390)
    expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(844)
    const searchScreenshot = testInfo.outputPath('rules-node-search-mobile.png')
    await page.screenshot({path: searchScreenshot})
    await testInfo.attach('窄屏节点快捷搜索', {path: searchScreenshot, contentType: 'image/png'})
    await page.getByTestId('node-search-action-rewrite_model').click()
    await expect(nodeSearch(page)).toHaveCount(0)
    await inlineNodeControls(page).getByLabel('当前模型', {exact: true}).fill('fixture-mobile-inline')
    await expect(nodeParameters(page)).not.toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(392)
    await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
    await expect.poll(() => admin.rules.find(rule => rule.name === 'Comfy narrow draft')?.actions[0]?.params.model).toBe('fixture-mobile-inline')
})
