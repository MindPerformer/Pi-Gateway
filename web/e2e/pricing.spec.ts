import {test, expect, type Page} from '@playwright/test'
import type {ModelPrice, ModelView, SyncRun} from '../src/api/pricing'

const price = (model: string): ModelPrice => ({model, standard: {input: '1.25', cached_input: '', cache_write: '0', output: '5'}, flex: null, fast: null, long_standard: null, long_flex: null, long_fast: null, context_mode: 'same', context_threshold: 200000, context_comparison: 'gt', disabled: false})
async function setup(page: Page, locale = 'zh-CN', syncFails = false) {
 let revision = 1
 let sources = [{id: 'fixture-source', name: 'Fixture source', url: 'https://example.test/prices.json', format: 'standard', enabled: true, interval_hours: 24, auto_apply: false, last_error: ''}]
 let items: ModelView[] = [{model: 'fixture-model', effective: price('fixture-model'), builtin: price('fixture-model'), manual: null, external: null, status: 'priced', source: 'builtin'}]
 const calls: Array<{path: string; body: any}> = []
 const errors: string[] = []
 page.on('pageerror', e => errors.push(e.message))
 const run: SyncRun = {id: 'run-1', source_id: 'fixture-source', created_at: 1794268800000, status: 'preview', base_revision: 1, hash: 'fixture-hash', items: [{model: 'fixture-model', before: price('fixture-model'), after: price('fixture-model'), warnings: [], locked: true}, {model: 'new-external', before: null, after: price('new-external'), warnings: ['Check cached input'], locked: false}]}
 await page.addInitScript(({locale}) => {localStorage.setItem('pi-gateway-token', 'public-pricing-fixture'); localStorage.setItem('pi-gateway-locale', locale); localStorage.setItem('pi-gateway-theme', 'light')}, {locale})
 await page.route(url => url.pathname.startsWith('/api/'), async route => {
  const req = route.request(), path = new URL(req.url()).pathname, body = req.postData() ? req.postDataJSON() : undefined
  calls.push({path, body})
  const reply = (json: unknown, status = 200) => route.fulfill({json, status})
  if (path === '/api/auth/me') return reply({username: 'admin'})
  if (path === '/api/pricing/models' && req.method() === 'GET') return reply({revision, items})
  if (path === '/api/pricing/models') {expect(body.expected_revision).toBe(revision); revision++; const p = body.price as ModelPrice; const row: ModelView = {model: p.model, effective: p, manual: p, builtin: null, external: null, status: 'priced', source: 'manual'}; items = [...items.filter(v => v.model !== p.model), row]; return reply({revision})}
  if (path === '/api/pricing/sources' && req.method() === 'GET') return reply({revision, items: sources})
  if (path === '/api/pricing/sources') {expect(body.expected_revision).toBe(revision); sources = [...sources.filter(v => v.id !== body.source.id), body.source]; return reply({revision: ++revision})}
  if (path === '/api/pricing/sources/fixture-source/sync') {
   if (syncFails) {revision++; sources[0]!.last_error = 'Source fetch failed'; return reply({error: 'Source fetch failed'}, 502)}
   return reply({revision, run})
  }
  if (path === '/api/pricing/sync-runs') return reply({revision, items: []})
  if (path === '/api/pricing/sync-runs/run-1/apply') {expect(body.models).toEqual(['new-external']); return reply({revision: ++revision})}
  if (path === '/api/pricing/history') return reply({revision, items: []})
  if (path === '/api/pricing/preview') return reply({billing: {total_cost_micros: 1250000}})
  if (path === '/api/pricing/recalculate/preview') return reply({revision, preview_token: 'preview-fixture', matched: 4, eligible: 2, already_repriced: 0, missing_usage: 1, unpriced: 1, cost_micros: 1250000, has_more: false, next_after_id: 4, items: [{id: 1, model: body.model || 'fixture-model', account_id: 7, cost_micros: 1250000, price_version: 'catalog-1'}]})
  if (path === '/api/pricing/recalculate') {expect(body.preview_token).toBe('preview-fixture'); return reply({revision, updated: 2, skipped: 0, cost_micros: 1250000, has_more: false, next_after_id: 4})}
  return reply({error: `Unhandled fixture: ${path}`}, 500)
 })
 await page.goto('/pricing')
 await expect(page.getByRole('button', {name: '新增模型', exact: true}).or(page.getByRole('button', {name: 'Add model', exact: true}))).toBeVisible()
 return {calls, errors}
}

test('create, full context edit, fee preview, manual save and copy new model ID', async ({page}) => {
 const {calls, errors} = await setup(page)
 await page.getByRole('button', {name: '新增模型', exact: true}).click()
 let dialog = page.getByRole('dialog')
 await dialog.getByLabel('模型 ID', {exact: true}).fill('new-manual')
 await dialog.getByLabel('标准 · 输入', {exact: true}).fill('0.123456789')
 await dialog.getByLabel('标准 · 输出', {exact: true}).fill('0')
 await dialog.getByLabel('显式别名（每行一个）').fill('explicit-alias')
 await dialog.getByLabel('长上下文规则').selectOption('custom')
 const longTier = dialog.locator('fieldset').filter({has: page.locator('legend', {hasText: '长上下文 · 标准'})})
 await longTier.getByLabel('为此档位定价').check()
 await dialog.getByLabel('长上下文 · 标准 · 输入', {exact: true}).fill('2.5')
 await dialog.getByLabel('输入 Token 阈值', {exact: true}).fill('200000')
 await dialog.getByLabel('阈值比较方式').selectOption('gte')
 await dialog.getByRole('button', {name: '试算费用'}).click()
 await expect(dialog.getByText('预估 USD: $1.25')).toBeVisible()
 await dialog.getByRole('button', {name: '保存手动价格', exact: true}).click()
 await expect(dialog).not.toBeVisible()
 const save = calls.find(v => v.path === '/api/pricing/models' && v.body)?.body
 expect(save.price.standard).toEqual({input: '0.123456789', cached_input: '', cache_write: '', output: '0'})
 expect(save.price.aliases).toEqual(['explicit-alias']); expect(save.price.context_comparison).toBe('gte')
 const card = page.locator('article').filter({has: page.locator('strong', {hasText: /^new-manual$/})})
 await expect(card.getByText('手动锁定')).toBeVisible()
 await card.getByRole('button', {name: '设置 / 编辑价格'}).click()
 dialog = page.getByRole('dialog')
 await expect(dialog.getByLabel('模型 ID', {exact: true})).toHaveAttribute('readonly')
 await dialog.getByLabel('标准 · 输入', {exact: true}).fill('3.5')
 await dialog.getByRole('button', {name: '保存手动价格', exact: true}).click()
 await expect(card.getByText('$3.5')).toBeVisible()
 await card.getByRole('button', {name: '复制规则'}).click()
 dialog = page.getByRole('dialog')
 await expect(dialog.getByLabel('模型 ID', {exact: true})).toHaveValue('')
 await dialog.getByLabel('模型 ID', {exact: true}).fill('copied-model')
 await dialog.getByRole('button', {name: '保存手动价格', exact: true}).click()
 await expect(page.locator('article').filter({has: page.locator('strong', {hasText: /^copied-model$/})})).toBeVisible()
 expect(errors).toEqual([])
})

test('sync preview cannot select manual locks and applies only reviewed models', async ({page}) => {
 const {calls, errors} = await setup(page)
 await page.getByRole('tab', {name: '外部来源'}).click()
 await page.getByRole('button', {name: '拉取同步预览'}).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByRole('checkbox', {name: '选择模型 fixture-model', exact: true})).toBeDisabled()
 await dialog.getByRole('checkbox', {name: '选择所有未锁定模型'}).check()
 await expect(dialog.getByRole('checkbox', {name: '选择模型 new-external', exact: true})).toBeChecked()
 await dialog.getByRole('button', {name: '应用所选模型 (1)'}).click()
 await expect(dialog).not.toBeVisible()
 expect(calls.some(v => v.path.endsWith('/run-1/apply'))).toBe(true)
 expect(errors).toEqual([])
})

test('recalculation preserves filter and preview token, invalidates stale preview on edit', async ({page}) => {
 const {calls, errors} = await setup(page)
 await page.getByRole('button', {name: '补算额度预估', exact: true}).first().click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText(/保留原始费用和 API Key 预算，不追扣/)).toBeVisible()
 await dialog.getByLabel('模型 ID', {exact: true}).fill('fixture-model')
 await dialog.getByLabel('账号 ID（可选）').fill('7')
 await dialog.getByRole('button', {name: '预览缺价补算'}).click()
 await expect(dialog.getByText('$1.250000', {exact: true})).toBeVisible()
 await dialog.getByLabel('账号 ID（可选）').fill('8')
 await expect(dialog.getByRole('button', {name: '确认补算预估'})).not.toBeVisible()
 await dialog.getByRole('button', {name: '预览缺价补算'}).click()
 await dialog.getByRole('button', {name: '确认补算预估'}).click()
 await expect(dialog.getByRole('status')).toContainText('已更新 2 条预估')
 expect(calls.find(v => v.path === '/api/pricing/recalculate')?.body).toMatchObject({model: 'fixture-model', account_id: 8, preview_token: 'preview-fixture', expected_revision: 1})
 expect(errors).toEqual([])
})

test('failed sync reloads persisted source error and revision before the next save', async ({page}) => {
 const {calls, errors} = await setup(page, 'zh-CN', true)
 await page.getByRole('tab', {name: '外部来源'}).click()
 await page.getByRole('button', {name: '拉取同步预览'}).click()
 await expect(page.locator('article').filter({hasText: 'Fixture source'}).getByText('Source fetch failed', {exact: true})).toBeVisible()
 await expect(page.getByText('目录版本 2', {exact: true})).toBeVisible()
 await page.getByRole('button', {name: '新增来源'}).click()
 const dialog = page.getByRole('dialog')
 await dialog.getByLabel('来源 ID').fill('models-dev')
 await dialog.getByLabel('显示名称').fill('Models development')
 await dialog.getByLabel('格式', {exact: true}).selectOption('models_dev')
 await expect(dialog.getByLabel('来源 URL', {exact: true})).toHaveValue('https://models.dev/api.json')
 await expect(dialog.getByLabel('同步间隔（小时）')).toHaveValue('24')
 await expect(dialog.getByLabel('自动应用已跟随的模型')).not.toBeChecked()
 await expect(dialog.getByLabel('可信内网主机（每行一个）')).toHaveValue('')
 await dialog.getByRole('button', {name: '保存来源'}).click()
 await expect(dialog).not.toBeVisible()
 expect(calls.find(v => v.path === '/api/pricing/sources' && v.body)?.body).toMatchObject({expected_revision: 2, source: {auto_apply: false, trusted_hosts: [], url: 'https://models.dev/api.json', interval_hours: 24}})
 expect(errors).toEqual([])
})

test('English dark mobile pricing remains accessible without overflow', async ({page}) => {
 await page.setViewportSize({width: 390, height: 844})
 const {errors} = await setup(page, 'en')
 await page.getByRole('button', {name: 'Open navigation'}).click()
 await page.getByRole('button', {name: 'Switch to dark mode'}).click()
 await page.getByRole('link', {name: 'Model pricing', exact: true}).click()
 await page.getByRole('button', {name: 'Add model', exact: true}).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByLabel('Model ID', {exact: true})).toBeVisible()
 expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
 await page.keyboard.press('Escape')
 await expect(dialog).not.toBeVisible()
 expect(errors).toEqual([])
})
