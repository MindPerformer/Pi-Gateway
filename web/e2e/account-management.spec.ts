import {expect, type Page, test} from '@playwright/test'
import type {Account} from '../src/api/types'

const token = 'public-admin-fixture-token'
const sample = (id: number): Account => ({
    id, name: `Account ${id}`, email: `account${id}@example.test`, account_id: `chatgpt:account${id}@example.test`,
    plan_type: 'plus', expires_at: 1791446400000, enabled: true, weight: 1, concurrency: 3,
    proxy_url: '', proxy_id: null, proxy_display: 'direct', upstream_protocol: '', codex_linked: false,
    codex_account_id: '', status: 'ready', last_error: '', last_refresh_at: 0, last_used_at: 0,
    request_count: 0, error_count: 0, created_at: 0, updated_at: 0, inflight: 0, has_refresh_token: true,
    has_chatgpt_credential: true, token_expires_in_ms: 1000, quota: {} as Account['quota'],
    group_ids: [1], disabled_models: [], inherited_model_restrictions: [],
})

async function fixture(page: Page, count = 23, meStatus = 200) {
    let accounts = Array.from({length: count}, (_, i) => sample(i + 1))
    const requests: Array<{ path: string; body: any }> = []
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(({token}) => {
        if (!localStorage.getItem('fixture-initialized')) {
            localStorage.setItem('pi-gateway-token', token)
            localStorage.setItem('pi-gateway-locale', 'zh-CN')
            localStorage.setItem('pi-gateway-theme', 'light')
            localStorage.setItem('fixture-initialized', 'true')
        }
    }, {token})
    await page.route(url => url.pathname.startsWith('/api/'), async route => {
        const req = route.request(), path = new URL(req.url()).pathname
        const body = req.postData() ? req.postDataJSON() : undefined
        requests.push({path, body})
        const reply = (json: unknown, status = 200) => route.fulfill({json, status})
        if (path === '/api/auth/me') {
            if (meStatus === -1) return route.abort('connectionrefused')
            return reply(meStatus === 200 ? {username: 'fixture-admin'} : {error: meStatus === 401 ? 'authentication required' : 'could not validate session; retry shortly'}, meStatus)
        }
        if (path === '/api/auth/logout') return reply({ok: true})
        if (path === '/api/accounts') return reply({accounts})
        if (path === '/api/account-groups') return reply({
            groups: [
                {
                    id: 1,
                    name: 'Existing group',
                    enabled: true,
                    account_ids: accounts.map(a => a.id),
                    disabled_models: []
                },
                {id: 2, name: 'New group', enabled: true, account_ids: [], disabled_models: []},
            ]
        })
        if (path === '/api/proxies') return reply({items: [], total: 0, page: 1, page_size: 100})
        if (path === '/api/settings') return reply({current: {upstream_transport: 'sse'}, defaults: {}})
        if (path === '/api/accounts/batch') {
            accounts = accounts.map(a => body.ids.includes(a.id) ? {
                ...a,
                enabled: body.action === 'enable' ? true : body.action === 'disable' ? false : a.enabled,
                group_ids: body.action === 'add_groups' ? [...new Set([...a.group_ids, ...body.group_ids])] : a.group_ids,
            } : a)
            return reply({
                results: body.ids.map((id: number, i: number) => ({
                    index: i,
                    id,
                    name: `Account ${id}`,
                    status: 'success'
                }))
            })
        }
        if (path === '/api/accounts/export') return reply({
            type: 'pi-gateway-accounts',
            version: 1,
            accounts: body.ids.map((id: number) => ({
                email: `account${id}@example.test`,
                chatgpt: {refresh_token: 'public-test-placeholder'}
            }))
        })
        if (path === '/api/accounts/import') {
            accounts = [...accounts, {...sample(24), enabled: false, has_chatgpt_credential: false, codex_linked: true}]
            return reply({
                format: 'cliproxyapi',
                results: [{index: 0, id: 24, name: 'Account 24', status: 'created'}, {
                    index: 1,
                    status: 'failed',
                    error: 'missing refresh_token'
                }]
            })
        }
        if (path === '/api/oauth/start') return reply({
            flow: {
                id: 'public-fixture-flow',
                kind: 'chatgpt',
                status: 'pending',
                auth_url: 'https://example.test/oauth',
                callback_available: false
            }
        })
        if (path === '/api/oauth/public-fixture-flow') return reply({
            flow: {
                id: 'public-fixture-flow',
                status: 'pending'
            }
        })
        errors.push(`Unexpected request: ${path}`)
        return reply({error: 'unmocked API'}, 501)
    })
    await page.route(url => url.pathname.startsWith('/v1/'), route => route.abort('blockedbyclient'))
    return {requests, errors}
}

test('跨页选择、批量加入分组与禁用，导出使用实际选中 ID', async ({page}) => {
    const state = await fixture(page)
    await page.goto('/accounts')
    await page.getByRole('checkbox', {name: '选择账号 Account 1', exact: true}).check()
    await page.getByRole('button', {name: '下一页', exact: true}).click()
    await page.getByRole('checkbox', {name: '选择账号 Account 21', exact: true}).check()
    await page.getByRole('button', {name: '批量加入分组', exact: true}).click()
    await page.getByRole('checkbox', {name: 'New group', exact: true}).check()
    await page.getByRole('button', {name: '执行', exact: true}).click()
    const result = page.getByRole('dialog', {name: '操作结果'})
    await expect(result).toContainText('成功 2')
    expect(state.requests.find(r => r.path === '/api/accounts/batch')?.body).toEqual({
        ids: [1, 21],
        action: 'add_groups',
        group_ids: [2]
    })
    await result.getByRole('button', {name: '关闭', exact: true}).last().click()
    await expect(page.getByRole('checkbox', {name: '选择账号 Account 21', exact: true})).toBeChecked()
    await page.getByRole('button', {name: '批量禁用', exact: true}).click()
    await expect(result).toContainText('成功 2')
    await result.getByRole('button', {name: '关闭', exact: true}).last().click()
    const download = page.waitForEvent('download')
    await page.getByRole('button', {name: '导出所选账号', exact: true}).click()
    expect((await download).suggestedFilename()).toBe('pi-gateway-accounts.json')
    expect(state.requests.find(r => r.path === '/api/accounts/export')?.body.ids).toEqual([1, 21])
    expect(state.errors).toEqual([])
})

test('本页全选、全筛选选择与清除选择可区分，筛选不会丢失已选项', async ({page}) => {
    const state = await fixture(page)
    await page.goto('/accounts')
    await page.getByRole('checkbox', {name: '选择本页账号', exact: true}).check()
    await expect(page.getByRole('button', {name: '选择全部筛选结果（23）', exact: true})).toBeVisible()
    await page.getByRole('button', {name: '选择全部筛选结果（23）', exact: true}).click()
    await page.getByRole('textbox', {name: /搜索/}).fill('Account 23')
    await expect(page.getByRole('checkbox', {name: '选择账号 Account 23', exact: true})).toBeChecked()
    await page.getByRole('button', {name: '批量恢复', exact: true}).click()
    await expect(page.getByRole('dialog', {name: '操作结果'})).toContainText('成功 23')
    expect(state.requests.find(r => r.path === '/api/accounts/batch')?.body.ids).toHaveLength(23)
    await page.getByRole('dialog').getByRole('button', {name: '关闭', exact: true}).last().click()
    await page.getByRole('button', {name: '清除选择', exact: true}).click()
    await expect(page.getByRole('button', {name: '批量恢复', exact: true})).toHaveCount(0)
    await expect(page.getByRole('button', {name: '导出全部账号', exact: true})).toBeVisible()
    expect(state.errors).toEqual([])
})

test('导入文件显示逐条成功失败，凭证账号可关联 ChatGPT；窄屏弹窗不溢出', async ({page}) => {
    const state = await fixture(page, 2)
    await page.setViewportSize({width: 390, height: 844})
    await page.goto('/accounts')
    await page.getByRole('button', {name: '导入账号', exact: true}).click()
    const dialog = page.getByRole('dialog', {name: '导入账号'})
    await dialog.getByLabel('或粘贴 JSON 内容').fill('invalid')
    await dialog.getByRole('button', {name: '导入账号', exact: true}).click()
    await expect(page.getByRole('dialog', {name: '操作结果'})).toContainText('JSON 无效')
    expect(state.requests.filter(r => r.path === '/api/accounts/import')).toHaveLength(0)
    await page.getByRole('dialog').getByRole('button', {name: '关闭', exact: true}).last().click()
    await page.getByRole('button', {name: '导入账号', exact: true}).click()
    await dialog.getByLabel('选择 JSON 文件（可多选）').setInputFiles({
        name: 'codex-fixture.json',
        mimeType: 'application/json',
        buffer: Buffer.from(JSON.stringify([{
            type: 'codex',
            access_token: 'public-fixture',
            refresh_token: 'public-fixture',
            account_id: 'public-id'
        }]))
    })
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await dialog.getByRole('button', {name: '导入账号', exact: true}).click()
    await expect(page.getByRole('dialog', {name: '操作结果'})).toContainText('成功 1 · 跳过 0 · 失败 1')
    await page.getByRole('dialog').getByRole('button', {name: '关闭', exact: true}).last().click()
    await page.getByRole('button', {name: '关联 ChatGPT', exact: true}).click()
    await page.getByRole('dialog').getByRole('button', {name: '开始登录', exact: true}).click()
    await expect.poll(() => state.requests.find(r => r.path === '/api/oauth/start')?.body.account_id).toBe('24')
    expect(state.errors).toEqual([])
})

test('恢复登录遇到重启期间 503 保留 token，刷新后仍可登录', async ({page}) => {
    const state = await fixture(page, 1, 503)
    await page.goto('/accounts')
    await expect(page.getByRole('heading', {name: '账号管理', exact: true})).toBeVisible()
    expect(await page.evaluate(() => localStorage.getItem('pi-gateway-token'))).toBe(token)
    await page.reload()
    await expect(page.getByRole('heading', {name: '账号管理', exact: true})).toBeVisible()
    expect(await page.evaluate(() => localStorage.getItem('pi-gateway-token'))).toBe(token)
    expect(state.requests.some(r => r.path === '/api/auth/logout')).toBe(false)
    expect(state.errors).toEqual([])
})

test('恢复登录遇到明确 401 清除 token 并回到登录页', async ({page}) => {
    const state = await fixture(page, 1, 401)
    await page.goto('/accounts')
    await expect(page).toHaveURL(/\/login/)
    expect(await page.evaluate(() => localStorage.getItem('pi-gateway-token'))).toBeNull()
    expect(state.requests.some(r => r.path === '/api/auth/logout')).toBe(false)
    expect(state.errors).toEqual([])
})

test('恢复登录遇到网络错误保留 token，批量工具与导入窗口随语言切换', async ({page}) => {
    const state = await fixture(page, 1, -1)
    await page.goto('/accounts')
    await page.getByRole('checkbox', {name: '选择账号 Account 1', exact: true}).check()
    expect(await page.evaluate(() => localStorage.getItem('pi-gateway-token'))).toBe(token)
    await page.getByLabel('语言', {exact: true}).selectOption('en')
    await expect(page.getByRole('button', {name: 'Enable selected', exact: true})).toBeVisible()
    await page.getByRole('button', {name: 'Import accounts', exact: true}).click()
    const dialog = page.getByRole('dialog', {name: 'Import accounts'})
    await expect(dialog.getByLabel('Source format')).toContainText('Pi Gateway standard v1')
    await expect(dialog).toContainText('Unmatched credentials create disabled accounts')
    expect(state.errors).toEqual([])
})
