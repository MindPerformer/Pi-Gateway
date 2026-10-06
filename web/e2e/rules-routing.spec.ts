import {expect, test} from './admin.fixture'

test('HTTP 管理员登录重定向到规则页，目录来自真实 Go 注册表', async ({page, admin}) => {
    await page.addInitScript(() => localStorage.removeItem('pi-gateway-token'))
    await page.goto('/rules')
    await expect(page).toHaveURL(/\/login\?redirect=\/rules/)
    await page.locator('#username').fill('fixture-admin')
    await page.locator('#password').fill('fixture-only-password')
    await page.locator('button[type="submit"]').click()
    await expect(page).toHaveURL(/\/rules$/)
    await expect(page.getByRole('heading', {name: 'Fixture request rule', exact: true})).toBeVisible()
    expect(new URL(page.url()).protocol).toBe('http:')
    expect(admin.requests.find(request => request.path === '/api/auth/login')?.body).toEqual({
        username: 'fixture-admin', password: 'fixture-only-password',
    })
    expect(admin.requests.some(request => request.path === '/api/rules/schema')).toBe(true)
    await expect(page.getByRole('alert')).toHaveCount(0)
})

test('旧中间件地址保留规则路由兼容', async ({page, admin}) => {
    await admin.open('/middlewares')
    await expect(page).toHaveURL(/\/rules$/)
    await expect(page.getByRole('heading', {name: 'Fixture event rule', exact: true})).toBeVisible()
})
