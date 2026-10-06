import {expect, type Locator, type Page} from '@playwright/test'
import type {Rule} from '../src/api/rules'

export const ruleEditor = (page: Page) => page.locator('form.rule-editor')
export const nodeParameters = (page: Page) => page.getByTestId('node-parameters')
export const ruleName = (page: Page) => ruleEditor(page).locator('[data-rule-path="/name"] textarea').first()
export const selectedNode = (page: Page) => page.locator('.vue-flow__node.selected .rule-graph-node')
export const inlineNodeControls = (page: Page, node: Locator = selectedNode(page)) => node.getByTestId('node-inline')
export const nodeSearch = (page: Page) => page.getByTestId('node-search')

export async function openAdvanced(page: Page, node: Locator = selectedNode(page)) {
    await closeParameters(page)
    await page.getByTestId('canvas-fit').click()
    await node.getByTestId('node-advanced').click()
    await expect(nodeParameters(page)).toBeVisible()
}

export async function closeParameters(page: Page) {
    const close = nodeParameters(page).getByRole('button', {name: /^(关闭|Close)$/})
    if (await close.isVisible()) await close.click()
}

export async function addModule(page: Page, kind: 'condition' | 'action', type: string) {
    await closeParameters(page)
    const button = page.getByTestId(`add-${kind}-${type}`)
    if (!await button.isVisible()) await page.getByTestId('toggle-module-library').click()
    await button.click()
}

export async function readCode(page: Page): Promise<Rule> {
    await closeParameters(page)
    await ruleEditor(page).getByRole('button', {name: /^(JSON 代码|JSON code)$/}).click()
    const input = page.getByTestId('rule-code')
    await expect(input).toBeVisible()
    return JSON.parse(await input.inputValue())
}

export async function showCanvas(page: Page) {
    await ruleEditor(page).getByRole('button', {name: /^(图形编辑|Visual editor)$/}).click()
    await expect(page.getByTestId('rule-canvas')).toBeVisible()
}

export async function selectCapture(page: Page, id = 101) {
    const choice = page.getByTestId(`capture-choice-${id}`)
    if (!await choice.isVisible()) await page.getByTestId('choose-capture').click()
    await choice.click()
    await expect(page.getByTestId('simulate-capture')).toBeEnabled()
}

export async function runSimulation(page: Page) {
    await page.getByTestId('simulate-capture').click()
    await expect(page.getByTestId('simulation-result')).toBeVisible()
    await expect(page.getByTestId('simulation-stale')).toHaveCount(0)
}
