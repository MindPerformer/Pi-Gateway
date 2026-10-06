import {expect, simulationResponse, test} from './admin.fixture'
import {ruleEditor, ruleName, selectCapture} from './rules-ui.helpers'

test.describe('记录调试器', () => {
    test('从已有记录一键载入请求样本、显示前后 diff、步骤并联动画布节点', async ({page, admin}) => {
        await admin.open()
        await admin.editRule('Fixture request rule')
        await selectCapture(page, 101)
        await expect(page.getByTestId('capture-choice-101')).toHaveCount(0)
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulation-result')).toBeVisible()
        await expect(page.getByTestId('simulation-diff')).toBeVisible()
        await expect(page.getByTestId('simulation-diff')).toContainText('规则处理前')
        await expect(page.getByTestId('simulation-diff')).toContainText('fixture-old-model')
        await expect(page.getByTestId('simulation-diff')).toContainText('fixture-new-model')
        await expect(page.getByTestId('rule-debugger').locator('textarea')).toHaveCount(0)
        expect(admin.requests.some(request => request.path === '/api/captures/101')).toBe(true)
        await page.getByTestId('simulation-tab-fields').click()
        await expect(page.getByTestId('simulation-fields')).toContainText('/model')
        await page.getByTestId('simulation-tab-steps').click()
        await expect(page.getByTestId('simulation-steps')).toBeVisible()
        await expect(page.getByTestId('simulation-step')).toHaveCount(2)
        await expect(page.getByTestId('simulation-condition')).toHaveCount(3)
        await page.getByTestId('simulation-condition').nth(1).click()
        await expect(page.locator('.vue-flow__node.selected')).toHaveCount(1)
        await expect(page.locator('.vue-flow__node.selected .rule-graph-node-status')).toHaveText('已命中')
        await expect(page.getByTestId('simulation-condition').nth(2)).toContainText('已跳过')
        await page.getByTestId('simulation-step').filter({hasText: '修改模型'}).click()
        await expect(page.locator('.vue-flow__node.selected .rule-graph-node-title')).toHaveText('修改模型')
        await page.getByLabel('语言', {exact: true}).selectOption('en')
        await expect(page.getByTestId('simulation-steps')).toContainText('Set field value')
        await expect(page.getByTestId('simulation-condition').nth(2)).toContainText('Skipped')
        expect(await page.getByTestId('simulation-steps').innerText()).not.toMatch(/\b(json_set|rewrite_model|matched|skipped)\b/)
        expect(admin.requests.filter(request => request.method !== 'GET').map(request => request.path)).toEqual(['/api/rules/simulate-capture'])
        const request = admin.simulationRequests()[0]!
        expect(request.capture_id).toBe(101)
        expect(request.scope).toBe('ruleset')
        expect(request.phase).toBe('request')
        expect(request).not.toHaveProperty('input')
        expect(request.rule).toHaveProperty('actions')
        expect(admin.unexpected).toEqual([])
    })

    test('样本、范围或草稿变化后旧结果过期，不能伪装成当前结果', async ({page, admin}) => {
        await admin.open()
        await admin.editRule('Fixture request rule')
        await selectCapture(page, 101)
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulation-result')).toBeVisible()
        await page.getByTestId('simulation-scope').selectOption('rule')
        await expect(page.getByTestId('simulation-stale')).toBeVisible()
        await expect(page.getByTestId('simulate-capture')).toBeEnabled()
        await expect(page.getByTestId('simulation-result')).toContainText('结果已过期')
        const beforeCount = admin.simulationRequests().length
        await page.getByTestId('simulate-capture').click()
        await expect.poll(() => admin.simulationRequests().length).toBe(beforeCount + 1)
        await expect(page.getByTestId('simulation-stale')).toHaveCount(0)
        await expect(page.getByTestId('simulation-result')).toContainText('规则集版本 7')
        await ruleName(page).fill('Changed after simulation')
        await expect(page.getByTestId('simulation-stale')).toBeVisible()
        await expect(page.locator('.rule-graph-node-status')).toHaveCount(0)
        await page.getByRole('button', {name: '关闭编辑器', exact: true}).click()
        await admin.editRule('Fixture event rule')
        await page.getByRole('button', {name: '关闭编辑器', exact: true}).click()
        await admin.editRule('Fixture request rule')
        await expect(ruleName(page)).toHaveValue('Changed after simulation')
        expect(admin.rules[0]!.name).toBe('Fixture request rule')
    })

    test('记录被清理时显示可读 404，草稿切换与并发冲突保留本地内容', async ({page, admin}) => {
        await admin.open()
        await admin.editRule('Fixture request rule')
        admin.deletedCaptures.add(101)
        await page.getByTestId('choose-capture').click()
        await page.getByTestId('capture-choice-101').click()
        await expect(page.getByRole('alert')).toContainText('传输记录已被清理')
        await page.getByRole('button', {name: '关闭编辑器', exact: true}).click()
        await admin.editRule('Fixture event rule')
        await expect(ruleEditor(page).getByRole('heading', {name: 'Fixture event rule', exact: true})).toBeVisible()
        await page.getByTestId('choose-capture').click()
        await page.getByTestId('capture-choice-102').click()
        await expect(page.getByTestId('capture-event')).toBeVisible()
        await page.getByRole('button', {name: '关闭编辑器', exact: true}).click()
        await admin.editRule('Fixture request rule')
        await ruleName(page).fill('Draft retained through conflict')
        admin.conflictNextSave = true
        await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
        await expect(ruleEditor(page).getByRole('alert')).toContainText('服务器规则已变化')
        await expect(ruleName(page)).toHaveValue('Draft retained through conflict')
        await expect(page.getByRole('button', {name: '保留草稿并确认新修订号', exact: true})).toBeVisible()
        page.once('dialog', dialog => dialog.accept())
        await page.getByRole('button', {name: '保留草稿并确认新修订号', exact: true}).click()
        await ruleEditor(page).getByRole('button', {name: '保存规则', exact: true}).click()
        await expect.poll(() => admin.rules[0]?.name).toBe('Draft retained through conflict')
        expect(admin.requests.filter(request => request.method === 'PUT').at(-1)!.body.expected_revision).toBe(2)
    })

    test('响应事件选择、批量分页和版本令牌都使用后端契约', async ({page, admin}) => {
        admin.captures[0]!.rules_version = 2 // Historical capture version must not constrain a fresh simulation.
        admin.simulate = request => ({body: simulationResponse(request, {hasMore: request.batch === true && request.after_seq === undefined})})
        await admin.open()
        await admin.editRule('Fixture event rule')
        await selectCapture(page, 101)
        const event = page.getByTestId('capture-event')
        await event.selectOption('2')
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulation-result')).toBeVisible()
        expect(admin.simulationRequests()[0]).toMatchObject({
            capture_id: 101,
            phase: 'response_event',
            frame_seq: 2,
            batch: false
        })
        await page.getByTestId('batch-events').check()
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulate-next-batch')).toBeVisible()
        const firstBatch = admin.simulationRequests()[1]!
        expect(firstBatch).toMatchObject({capture_id: 101, phase: 'response_event', batch: true, limit: 50})
        expect(firstBatch).not.toHaveProperty('after_seq')
        expect(firstBatch).not.toHaveProperty('expected_version')
        expect(firstBatch).not.toHaveProperty('batch_token')
        await page.getByTestId('simulate-next-batch').click()
        await expect.poll(() => admin.simulationRequests().length).toBe(3)
        const next = admin.simulationRequests()[2]!
        expect(next).toMatchObject({
            batch: true,
            after_seq: 1,
            limit: 50,
            expected_version: 7,
            batch_token: 'fixture-same-version-and-draft-token'
        })
        await expect(page.getByTestId('simulation-result')).toContainText('消息 #2')
        await page.getByTestId('capture-event').selectOption('3')
        await expect(page.getByTestId('simulation-stale')).toBeVisible()
    })

    test('批量结果可切换查看单事件，不改变输入选择或把结果标过期', async ({page, admin}) => {
        admin.simulate = request => {
            const response = simulationResponse(request, {hasMore: true})
            response.results.push(simulationResponse({...request, frame_seq: 2}).results[0]!)
            response.next_after_seq = 2
            return {body: response}
        }
        await admin.open()
        await admin.editRule('Fixture event rule')
        await selectCapture(page, 101)
        await page.getByTestId('batch-events').check()
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulation-result-event')).toBeVisible()
        await page.getByTestId('simulation-result-event').selectOption('1')
        await expect(page.getByTestId('simulation-diff')).toContainText('modified event 2')
        await expect(page.getByTestId('capture-event')).toHaveValue('1')
        await expect(page.getByTestId('simulation-stale')).toHaveCount(0)
        expect(admin.simulationRequests()).toHaveLength(1)
    })

    test('批量续页遇到规则集 409 时保留可读冲突和过期结果', async ({page, admin}) => {
        admin.simulate = request => request.after_seq === undefined
            ? {body: simulationResponse(request, {hasMore: true})}
            : {status: 409, body: {error: 'rules version conflict'}}
        await admin.open()
        await admin.editRule('Fixture event rule')
        await selectCapture(page, 101)
        await page.getByTestId('batch-events').check()
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulate-next-batch')).toBeEnabled()
        await page.getByTestId('simulate-next-batch').click()
        await expect(page.getByTestId('rule-debugger').getByRole('alert')).toContainText('已保存规则集已变化')
        await expect(page.getByTestId('simulation-stale')).toBeVisible()
        await expect(page.getByTestId('simulate-next-batch')).toBeDisabled()
        expect(admin.simulationRequests()).toHaveLength(2)
        expect(admin.requests.some(request => request.method === 'PUT')).toBe(false)
    })

    test('停用草稿仅显式临时启用，重建来源有说明且不发布规则', async ({page, admin}) => {
        admin.rules[0]!.enabled = false
        admin.simulate = request => ({body: simulationResponse(request, {source: 'reconstructed'})})
        await admin.open()
        await admin.editRule('Fixture request rule')
        await selectCapture(page, 101)
        await expect(page.getByTestId('enable-draft')).not.toBeChecked()
        await page.getByTestId('enable-draft').check()
        await page.getByTestId('simulate-capture').click()
        await expect(page.getByTestId('simulation-result')).toContainText('不是历史现场的精确回放')
        expect(admin.simulationRequests()[0]).toMatchObject({enable_draft: true, rule: {enabled: false}})
        expect(admin.rules[0]!.enabled).toBe(false)
        expect(admin.requests.filter(request => request.method !== 'GET').map(request => request.path)).toEqual(['/api/rules/simulate-capture'])
    })

    test('传输详情入口只携带记录身份并自动载入样本', async ({page, admin}) => {
        await page.goto('/captures/101')
        await page.getByTestId('debug-capture-rules').click()
        await expect(page).toHaveURL(/\/rules\?capture=101$/)
        // The entry chooses a sample, not a replacement rule or a new draft.
        await admin.editRule('Fixture request rule')
        await expect(page.getByTestId('rule-debugger')).toContainText('传输记录 #101')
        expect(new URL(page.url()).searchParams.has('body')).toBe(false)
        await expect(page.getByTestId('simulate-capture')).toBeEnabled()
        expect(admin.requests.some(request => request.path === '/api/captures/101')).toBe(true)
    })

    test('异步旧请求不会覆盖后选记录', async ({page, admin}) => {
        let releaseFirst!: () => void
        const firstResponse = new Promise<void>(resolve => {
            releaseFirst = resolve
        })
        let call = 0
        admin.simulate = async request => {
            call++
            if (call === 1) await firstResponse
            return {body: simulationResponse(request, {source: 'checkpoint'})}
        }
        await admin.open()
        await admin.editRule('Fixture request rule')
        await selectCapture(page, 101)
        const firstRun = page.getByTestId('simulate-capture').click()
        await expect.poll(() => admin.simulationRequests().length).toBe(1)
        await page.getByTestId('choose-capture').click()
        await page.getByTestId('capture-choice-102').click()
        releaseFirst()
        await firstRun
        await expect(page.getByText('传输记录 #102')).toBeVisible()
        await expect(page.getByTestId('simulation-result')).toHaveCount(0)
        expect(admin.simulationRequests()[0]!.capture_id).toBe(101)
    })
})
