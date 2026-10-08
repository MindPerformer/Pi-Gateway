import {expect, test} from '@playwright/test'

test('official usage switches periods, scopes accounts, and retains stale data on failed sync', async ({page}) => {
    const today = new Date().toISOString().slice(0, 10)
    const yesterday = new Date(Date.now() - 86400000).toISOString().slice(0, 10)
    const requests: URL[] = []
    const errors: string[] = []
    const account = {
        id: 7,
        name: 'Official fixture account',
        account_id: 'fixture',
        codex_linked: true,
        codex_account_id: 'workspace'
    }
    page.on('pageerror', err => errors.push(err.message))
    await page.addInitScript(() => {
        localStorage.setItem('pi-gateway-token', 'public-fixture-token')
        localStorage.setItem('pi-gateway-locale', 'zh-CN')
        localStorage.setItem('pi-gateway-theme', 'light')
    })
    await page.route(url => url.pathname.startsWith('/api/'), async route => {
        const url = new URL(route.request().url())
        requests.push(url)
        const reply = (json: unknown) => route.fulfill({json})
        if (url.pathname === '/api/auth/me') return reply({username: 'fixture'})
        if (url.pathname === '/api/accounts') return reply({accounts: [account]})
        if (url.pathname === '/api/keys') return reply({keys: []})
        if (url.pathname === '/api/stats/summary') return reply({
            success_count: 1,
            total_tokens: 300,
            input_tokens: 200,
            output_tokens: 100,
            cached_tokens: 50,
            avg_latency_ms: 2000
        })
        if (url.pathname === '/api/usage/records') return reply({records: [], total: 0})
        if (url.pathname === '/api/accounts/7/official-usage/refresh') return reply({
            sync: {},
            error: 'official usage returned HTTP 429'
        })
        if (url.pathname === '/api/usage/official') {
            const rows = [
                {
                    account_id: 7,
                    workspace_id: 'workspace',
                    day: today,
                    credits: 5,
                    uncached_input_tokens: 100,
                    cached_input_tokens: 20,
                    output_tokens: 30,
                    total_tokens: 150,
                    settled: false
                },
                {
                    account_id: 7,
                    workspace_id: 'workspace',
                    day: yesterday,
                    credits: 25,
                    uncached_input_tokens: 500,
                    cached_input_tokens: 200,
                    output_tokens: 300,
                    total_tokens: 1000,
                    settled: true
                },
            ].filter(d => d.day >= url.searchParams.get('start_date')! && d.day <= url.searchParams.get('end_date')!)
            return reply({
                items: rows,
                sync: [{workspace_id: 'workspace', synced_at: Date.now(), attempted_at: Date.now()}],
                total_credits: rows.reduce((sum, d) => sum + d.credits, 0),
                equivalent_usd: rows.reduce((sum, d) => sum + d.credits, 0) / 25,
                credits_per_usd: 25,
                missing_credit_days: 0,
                pending_days: rows.filter(d => !d.settled).length,
                workspace_count: 1
            })
        }
        errors.push(`Unexpected request: ${url.pathname}`)
        return route.fulfill({status: 501, json: {error: 'unmocked'}})
    })
    await page.goto('/usage')
    const panel = page.locator('.official-usage')
    await expect(panel.getByRole('heading', {name: '官方结算消耗'})).toBeVisible()
    await expect(panel.locator('tbody tr')).toHaveCount(2)
    await expect(panel.getByText('已结算', {exact: true})).toBeVisible()
    await expect(panel.getByText('待结算', {exact: true})).toBeVisible()
    await page.screenshot({path: 'node_modules/.cache/playwright/official-usage-desktop.png', fullPage: true})
    await panel.getByLabel('官方统计期间').selectOption('1')
    await expect(panel.locator('tbody tr')).toHaveCount(1)
    expect(requests.filter(u => u.pathname === '/api/usage/official').at(-1)?.searchParams.get('start_date')).toBe(today)
    await panel.getByLabel('官方统计期间').selectOption('7')
    await expect(panel.locator('tbody tr')).toHaveCount(2)
    await panel.getByLabel('官方统计账号').selectOption('7')
    await expect.poll(() => requests.filter(u => u.pathname === '/api/usage/official').at(-1)?.searchParams.get('account_id')).toBe('7')
    await panel.getByLabel('官方统计期间').selectOption('custom')
    await panel.getByLabel('开始日期').fill(yesterday)
    await panel.getByLabel('结束日期').fill(yesterday)
    await panel.getByRole('button', {name: '应用', exact: true}).click()
    await expect(panel.locator('tbody tr')).toHaveCount(1)
    await expect(panel.locator('tbody')).toContainText(yesterday)
    await panel.getByRole('button', {name: '同步官方数据'}).click()
    await expect(panel.getByRole('alert')).toContainText('HTTP 429')
    await expect(panel.locator('tbody tr')).toHaveCount(1)
    await page.setViewportSize({width: 390, height: 844})
    await page.screenshot({path: 'node_modules/.cache/playwright/official-usage-mobile.png', fullPage: true})
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    expect(errors).toEqual([])
})
