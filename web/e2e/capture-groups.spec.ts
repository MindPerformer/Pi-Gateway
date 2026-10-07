import {expect, test} from './admin.fixture'

test('大量规则执行和响应增量默认收纳，展开分批显示且保留终态', async ({page, admin}) => {
    const capture = structuredClone(admin.captures[0]!)
    capture.rule_traces = Array.from({length: 1000}, (_, i) => ({
        rule_id: 'grouped-rule',
        rule_name: 'Grouped rule',
        phase: 'response_event',
        action_id: 'set-delta',
        action_type: 'json_set',
        status: 'changed',
        event_type: 'response.output_text.delta',
        event_id: `event-${i}`,
        changes: [{
            path: '/delta',
            operation: 'replace',
            before_exists: true,
            after_exists: true,
            before: 'old',
            after: 'new'
        }],
    }))
    capture.response_frames = Array.from({length: 1000}, (_, i) => ({
        seq: i,
        dir: 'in' as const,
        kind: 'sse_event',
        type: 'response.output_text.delta',
        at_ms: i,
        bytes: 20,
        rule_event_id: `event-${i}`,
        data: {type: 'response.output_text.delta', item_id: 'item-1', delta: 'hello'}
    }))
    capture.response_frames.push({
        seq: 1001,
        dir: 'in',
        kind: 'sse_event',
        type: 'response.completed',
        at_ms: 1001,
        bytes: 20,
        data: {type: 'response.completed', response: {id: 'complete', output: []}}
    })
    admin.captures[0] = capture
    await page.goto('/captures/101')
    const rule = page.getByTestId('trace-rule-group')
    await expect(rule).toHaveCount(1)
    await expect(rule).toContainText('1000 次执行')
    await expect(page.locator('.trace-change')).toHaveCount(0)
    await rule.locator('summary').click()
    await expect(rule.locator('[data-rule-status]')).toHaveCount(1)
    await rule.locator('[data-rule-status] > summary').click()
    await expect(rule).toContainText('首条记录样例')
    await expect(rule.locator('.trace-change')).toHaveCount(1)
    const group = page.getByTestId('response-event-group')
    await expect(group).toHaveCount(1)
    await expect(group).toContainText('1000 条')
    await expect(page.locator('.individual-frame')).toHaveCount(1)
    await group.click()
    await expect(page.locator('.individual-frame')).toHaveCount(21)
    await expect(page.locator('.response-event-group').filter({has: group}).getByRole('button', {name: /980/})).toBeVisible()
})
