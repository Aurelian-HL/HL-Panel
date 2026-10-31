// Real Chrome acceptance with synthetic loopback API responses. Database,
// signatures and TLS verification have separate integration tests.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright')
const origin = process.env.WEB_URL || 'http://127.0.0.1:4197'
assert.ok(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), 'loopback required')
const output = path.resolve(process.env.SCREENSHOT_DIR || 'artifacts/license-shop-browser')
const products = [
  { id: 'monthly', name: '月度授权', term: { kind: 'calendar_months', months: 1 } },
  { id: 'quarterly', name: '季度授权', term: { kind: 'calendar_months', months: 3 } },
  { id: 'half-year', name: '半年授权', term: { kind: 'calendar_months', months: 6 } },
  { id: 'yearly', name: '年度授权', term: { kind: 'calendar_months', months: 12 } },
  { id: 'perpetual', name: '永久授权', term: { kind: 'perpetual' } },
]
const initial = { enabled: true, status: 'unactivated', management_allowed: false, domain: 'panel.example.test', expires_at: null, reminder_due: false, renewal_url: '' }
const active = { ...initial, status: 'active', management_allowed: true, license_id: 'lic-local-browser', tier: 'licensed', term: products[0].term, expires_at: '2027-01-31T16:00:00Z' }
const settings = { id: 'isolated', site_name: 'HL-PANEL', panel_title: 'HL 商店本地验收', public_description: '', support_url: '', theme: 'classic', background_image_url: '', revision: 1, updated_at: '2026-10-31T00:00:00Z' }
const code = 'hlr1_' + 'a'.repeat(32)

async function main() {
  await fs.mkdir(output, { recursive: true })
  const report = { synthetic: true, origin, started_at: new Date().toISOString(), checks: [], screenshots: [], errors: [], expected_http_errors: [] }
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH, headless: true })
  try {
    for (const viewport of [{ width: 1440, height: 1000 }, { width: 1024, height: 900 }, { width: 390, height: 844 }]) {
      const context = await browser.newContext({ viewport })
      await context.addInitScript(() => {
        sessionStorage.setItem('ny_admin_access_token', 'isolated-browser-token')
        sessionStorage.setItem('ny_admin_user', JSON.stringify({ id: 'adm-browser', username: '验收管理员', must_change_password: false }))
        sessionStorage.setItem('ny_admin_expires_at', '2099-01-01T00:00:00Z')
      })
      const page = await context.newPage()
      let state = '', status = { ...initial }, catalog = products, catalogMode = 'normal', redeemMode = 'success'
      let releaseCatalog, commits = 0, recoveries = 0, statusFailure = false
      page.on('pageerror', error => report.errors.push({ state, error: error.message }))
      page.on('console', message => {
        if (message.type() !== 'error') return
        const item = { state, message: message.text() }
        if (/Failed to load resource.*(409|502|503)/.test(item.message)) report.expected_http_errors.push(item)
        else report.errors.push(item)
      })
      await context.route('**/api/v1/**', async route => {
        const req = route.request(), suffix = new URL(req.url()).pathname.replace('/api/v1', '')
        const respond = (value, code = 200) => route.fulfill({ status: code, contentType: 'application/json', body: JSON.stringify(value) })
        switch (suffix) {
          case '/site-settings': return respond({ settings })
          case '/auth/me': return respond({ user: { id: 'adm-browser', username: '验收管理员', must_change_password: false } })
          case '/announcements': return respond({ items: [] })
          case '/public/site-info': return respond({ settings, announcements: [], platform_version: 'local-verification', build_time: null })
          case '/system/version': return respond({ current_version: 'local-verification', latest_version: 'local-verification', state: 'up_to_date', versions_behind: 0, attention_required: false })
          case '/license/status': return statusFailure ? respond({ error: { code: 'license_sync_failed', message: 'isolated outage' } }, 502) : respond(status)
          case '/license/shop':
            if (catalogMode === 'loading') await new Promise(resolve => { releaseCatalog = resolve })
            if (catalogMode === 'failure') return respond({ error: { code: 'license_shop_unavailable', message: 'isolated catalog failure' } }, 502)
            return respond({ items: catalog, redemption_enabled: catalogMode !== 'not-open' })
          case '/license/redemption':
            assert.equal(req.method(), 'POST')
            assert.equal(JSON.parse(req.postData()).code, code)
            commits++
            if (redeemMode === 'invalid') return respond({ error: { code: 'invalid_code', message: '兑换码无效，请核对购买记录中的完整兑换码' } }, 409)
            if (redeemMode === 'pending') {
              status = { ...status, redemption_pending: true }
              return respond({ error: { code: 'redemption_result_pending', message: '兑换结果尚未保存，请恢复兑换结果；不会重复续期' } }, 409)
            }
            status = { ...active }; return respond(status)
          case '/license/redemption/resume':
            assert.equal(req.method(), 'POST'); assert.equal(req.postData(), null)
            recoveries++; status = { ...active }; return respond(status)
          default: report.errors.push({ state, unexpected_api: suffix }); return respond({ error: { message: 'unmocked API' } }, 500)
        }
      })
      await context.route('**/*', async route => {
        const url = route.request().url()
        if (!url.startsWith(origin) && !url.startsWith('data:')) {
          report.errors.push({ state, external_request: url }); return route.abort()
        }
        return route.fallback()
      })
      const shop = page.locator('.shop-page')
      async function navigate(name) {
        state = name
        await page.goto(origin + '/shop', { waitUntil: 'domcontentloaded' })
        await shop.waitFor()
      }
      async function capture(name) {
        state = name
        const dimensions = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth }))
        assert.ok(dimensions.width <= dimensions.viewport + 1, name + ' document overflow: ' + JSON.stringify(dimensions))
        const overflow = await shop.evaluate(root => [...root.querySelectorAll('input, button, a, dd')].filter(element => {
          const rect = element.getBoundingClientRect()
          return rect.width && (rect.right > innerWidth + 1 || rect.left < -1)
        }).map(element => element.tagName + ':' + element.textContent))
        assert.deepEqual(overflow, [], name + ' control overflow')
        await page.evaluate(() => scrollTo(0, 0))
        const file = viewport.width + '-' + name + '.png'
        await page.screenshot({ path: path.join(output, file), fullPage: true })
        report.screenshots.push(file); report.checks.push({ width: viewport.width, state: name, passed: true })
      }
      catalogMode = 'loading'
      await navigate('loading')
      await shop.getByText('正在读取授权方案', { exact: true }).waitFor()
      await capture('loading')
      catalogMode = 'normal'; releaseCatalog()
      await shop.getByRole('heading', { name: '月度授权', exact: true }).waitFor()
      assert.equal(await shop.getByRole('button', { name: '购买入口待开放', exact: true }).count(), 5)
      assert.equal(await shop.getByText('价格待公布', { exact: true }).count(), 5)
      if (viewport.width < 560) await page.getByRole('button', { name: '展开菜单', exact: true }).click()
      await page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name: '商店', exact: true }).waitFor({ state: 'visible' })
      if (viewport.width < 560) await page.getByRole('button', { name: '收起菜单', exact: true }).click()
      await capture('unactivated-default-catalog')

      // Validate disabled purchases and secret input without an external request.
      assert.equal(await shop.locator('#shop-redemption-code').getAttribute('type'), 'password')
      await shop.locator('#shop-redemption-code').fill('bad-code')
      await shop.getByRole('button', { name: '激活兑换码', exact: true }).click()
      await shop.getByText('请填写购买获得的完整兑换码（hlr1_ 开头）。', { exact: true }).waitFor()
      assert.equal(commits, 0)
      await capture('validation-error')
      redeemMode = 'invalid'
      await shop.locator('#shop-redemption-code').fill(code)
      await shop.getByRole('button', { name: '激活兑换码', exact: true }).click()
      await shop.getByText('兑换码无效，请核对购买记录中的完整兑换码', { exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').inputValue(), '')
      await capture('invalid-code')
      redeemMode = 'success'
      await shop.locator('#shop-redemption-code').fill(code)
      await shop.getByRole('button', { name: '激活兑换码', exact: true }).click()
      await shop.getByText('兑换成功，授权已生效。', { exact: true }).waitFor()
      await shop.getByRole('heading', { name: '正式授权', exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').inputValue(), '')
      assert.ok(!(await page.evaluate(() => JSON.stringify({ session: { ...sessionStorage }, local: { ...localStorage }, url: location.href }))).includes(code))
      await capture('activated')

      status = { ...active, status: 'expired', management_allowed: false }
      await navigate('expired'); await shop.getByRole('heading', { name: '授权已到期', exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').isDisabled(), false)
      await capture('expired-renewal-available')
      redeemMode = 'pending'
      await shop.locator('#shop-redemption-code').fill(code)
      await shop.getByRole('button', { name: '激活兑换码', exact: true }).click()
      await shop.getByRole('button', { name: '恢复兑换结果', exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').count(), 0)
      await capture('redemption-pending')
      await shop.getByRole('button', { name: '恢复兑换结果', exact: true }).click()
      await shop.getByText('兑换结果已同步，请查看上方授权状态。', { exact: true }).waitFor()
      assert.equal(recoveries, 1)
      await capture('recovered')

      catalogMode = 'failure'
      await navigate('catalog-failure')
      await shop.getByRole('button', { name: '重新读取商品', exact: true }).waitFor()
      await capture('catalog-failure')
      assert.equal(await shop.locator('#shop-redemption-code').isDisabled(), true)
      catalogMode = 'not-open'
      await shop.getByRole('button', { name: '重新读取商品', exact: true }).click()
      await shop.getByText('商店尚未开通，暂不支持购买或兑换。', { exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').isDisabled(), true)
      assert.equal(await shop.getByRole('button', { name: '激活兑换码', exact: true }).isDisabled(), true)
      await capture('legacy-issuer-shop-not-open')
      catalogMode = 'normal'; catalog = []
      await navigate('empty-catalog')
      await shop.getByRole('heading', { name: '商店正在准备中', exact: true }).waitFor()
      await capture('empty-catalog')

      catalog = [{ ...products[0], price_label: '本地测试价格', purchase_url: 'https://store.example.test/monthly' }, { ...products[1], purchase_url: 'javascript:alert(1)' }]
      await navigate('configured-purchase')
      const purchase = shop.getByRole('link', { name: '购买月度授权', exact: true })
      await purchase.waitFor()
      assert.equal(await purchase.getAttribute('href'), 'https://store.example.test/monthly')
      assert.equal(await purchase.getAttribute('target'), '_blank')
      assert.equal(await purchase.getAttribute('rel'), 'noopener noreferrer')
      assert.equal(await shop.locator('a.shop-buy').count(), 1)
      await capture('configured-purchase-safe-links')

      catalog = products; status = { ...initial, enabled: false, status: 'unconfigured' }
      await navigate('unconfigured')
      await shop.getByRole('heading', { name: '授权服务未配置', exact: true }).waitFor()
      assert.equal(await shop.locator('#shop-redemption-code').isDisabled(), true)
      await capture('unconfigured')
      statusFailure = true
      await navigate('status-failure')
      await shop.getByText('授权状态暂未读到，请刷新状态后重试。', { exact: true }).waitFor()
      await capture('status-failure')
      statusFailure = false; status = { ...active, term: { kind: 'perpetual' }, expires_at: null }
      await shop.getByRole('button', { name: '刷新状态', exact: true }).click()
      await shop.getByText('长期有效', { exact: true }).waitFor()
      await capture('perpetual-status-recovered')
      await context.close()
    }
    assert.deepEqual(report.errors, [], 'unexpected browser errors')
    report.success = true
  } catch (error) { report.success = false; report.failure = error.stack; throw error }
  finally {
    await browser.close()
    report.finished_at = new Date().toISOString()
    await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2))
  }
  console.log(JSON.stringify({ success: report.success, checks: report.checks.length, screenshots: report.screenshots.length, errors: report.errors.length, output }))
}
main().catch(error => { console.error(error); process.exitCode = 1 })
