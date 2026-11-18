import {expect, test} from './admin.fixture'
import type {Settings, SettingsResponse} from '../src/api/types'

test('Codex 设置区分自动预览和固定版本，并原样保存自定义模板', async ({page, admin}, testInfo) => {
    const defaults: Settings = {
        upstream_transport: 'sse', capture_enabled: false, capture_limit: 100,
        default_model: 'fixture-model', model_mappings: {}, default_strategy: 'round-robin',
        max_concurrent_per_account: 2, refresh_margin_seconds: 60,
        reasoning_default_effort: '', reasoning_default_summary: '', default_proxy_url: '', user_agent: '',
        codex_client_version: 'auto', codex_user_agent_template: '',
    }
    const settings: SettingsResponse = {
        current: defaults, defaults, transports: ['sse'], strategies: ['round-robin'],
        static: {upstream_base_url: 'https://example.test', sse_zstd: false, oauth_callback_port: 1455,
            timezone: 'UTC', capture_persist: false, max_bytes_per_record: 65536, database: 'fixture.db'},
    }
    await page.route('**/api/settings', async route => {
        if (route.request().method() === 'PUT') {
            settings.current = route.request().postDataJSON()
            return route.fulfill({json: {settings: settings.current}})
        }
        return route.fulfill({json: settings})
    })
    await page.goto('/settings')

    const codex = page.getByRole('region', {name: 'Codex 客户端'})
    const mode = codex.getByLabel('客户端版本模式')
    const preview = codex.getByRole('status', {name: 'User-Agent 预览'})
    const save = page.getByRole('button', {name: '保存设置'})
    await expect(mode).toHaveValue('auto')
    await expect(preview).toHaveText('codex-tui/{version}')
    await expect(codex.getByText('此处不显示实际解析到的版本', {exact: false})).toBeVisible()
    await codex.screenshot({path: testInfo.outputPath('codex-auto.png')})

    await mode.selectOption('fixed')
    await expect(save).toBeDisabled()
    await codex.getByLabel('客户端版本', {exact: true}).fill('1.2.3-rc.1')
    await codex.getByLabel('客户端名称', {exact: true}).fill('my-codex')
    await codex.getByLabel('平台说明（选填）').fill('Linux; x64')
    await expect(preview).toHaveText('my-codex/1.2.3-rc.1 (Linux; x64)')
    await codex.screenshot({path: testInfo.outputPath('codex-fixed.png')})
    await save.click()
    await expect(save).toBeEnabled()
    expect(settings.current).toMatchObject({
        codex_client_version: '1.2.3-rc.1',
        codex_user_agent_template: 'my-codex/{version} (Linux; x64)',
    })

    await codex.getByRole('button', {name: '直接编辑模板'}).click()
    const template = codex.getByLabel('User-Agent 模板')
    await expect(template).toHaveValue('my-codex/{version} (Linux; x64)')
    for (const invalid of ['no-placeholder', '{version}/{version}', '{version} {extra}', '{version} 中文', ' client/{version}', `{version}${'x'.repeat(248)}`]) {
        await template.fill(invalid)
        await expect(template).toHaveAttribute('aria-invalid', 'true')
        await expect(save).toBeDisabled()
    }
    const custom = 'custom ({version}); platform'
    await template.fill(custom)
    await expect(preview).toHaveText('custom (1.2.3-rc.1); platform')
    await save.click()
    await expect(save).toBeEnabled()
    await page.reload()
    await expect(template).toHaveValue(custom)
    await expect(preview).toHaveText('custom (1.2.3-rc.1); platform')
    expect(settings.current.codex_user_agent_template).toBe(custom)

    await mode.selectOption('auto')
    await codex.getByRole('button', {name: '使用默认 User-Agent'}).click()
    await expect(preview).toHaveText('codex-tui/{version}')
    await save.click()
    await expect(save).toBeEnabled()
    expect(settings.current).toMatchObject({codex_client_version: 'auto', codex_user_agent_template: ''})
    expect(admin.pageErrors).toEqual([])
})
