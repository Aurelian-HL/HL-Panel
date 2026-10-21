// Real Chrome UI acceptance against loopback Vite and synthetic API fixtures.
// Protocol signatures, DNS and PostgreSQL are verified separately by runtime E2E.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright')
const origin = process.env.WEB_URL || 'http://127.0.0.1:4186'
assert.ok(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), 'UI tests require loopback')
const output = path.resolve(process.env.SCREENSHOT_DIR || 'artifacts/panel-license-browser')
const settings = { id: 'isolated', site_name: 'HL-PANEL', panel_title: 'HL 授权隔离验收', public_description: '', support_url: '', theme: 'classic', background_image_url: '', revision: 1, updated_at: '2026-10-20T08:00:00Z' }
const initial = { enabled: true, status: 'unactivated', management_allowed: false, domain: 'panel.example.test', expires_at: null, reminder_due: false, renewal_url: 'https://ht.hongle.work/' }
const free = { ...initial, license_id: 'lic-browser-free', status: 'active', tier: 'free', limits: { device_groups: 1, rules: 10 }, usage: { device_groups: 1, rules: 10 }, management_allowed: true, checked_at: '2026-10-20T08:00:00Z' }
const licensed = { ...free, license_id: 'lic-browser-paid', tier: 'licensed', limits: { device_groups: 0, rules: 0 }, expires_at: '2026-10-22T08:00:00Z' }
const proof = { purpose: 'free-registration', dns_name: '_hl-license.panel.example.test', dns_value: 'hl-license-v2=browser-fixture-'.repeat(3), expires_at: '2026-10-20T08:10:00Z' }

async function main() {
  await fs.mkdir(output, { recursive: true })
  const report = { synthetic: true, origin, started_at: new Date().toISOString(), checks: [], screenshots: [], errors: [], expected_failures: [] }
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
      let status = { ...initial }, state = '', commitAttempts = 0, dismissAttempts = 0, statusFailure = false, delay = 0
      page.on('pageerror', e => report.errors.push({ state, error: e.message }))
      page.on('console', m => {
        if (m.type() === 'error') {
          const item = { state, message: m.text() }
          if (/Failed to load resource.*502/.test(m.text())) report.expected_failures.push(item)
          else report.errors.push(item)
        }
      })
      await context.route('**/api/v1/**', async route => {
        const req = route.request(), suffix = new URL(req.url()).pathname.replace('/api/v1', '')
        const respond = (value, code = 200) => route.fulfill({ status: code, contentType: 'application/json', body: JSON.stringify(value) })
        switch (suffix) {
          case '/site-settings': return respond({ settings })
          case '/auth/me': return respond({ user: { id: 'adm-browser', username: '验收管理员', created_at: '2026-10-20T08:00:00Z', must_change_password: false } })
          case '/announcements': return respond({ items: [] })
          case '/public/site-info': return respond({ settings, announcements: [], platform_version: 'local-verification', build_time: null })
          case '/system/version': return respond({ current_version: 'local-verification', latest_version: 'local-verification', state: 'up_to_date', versions_behind: 0, can_defer: false, attention_required: false })
          case '/license/status':
            if (delay) await new Promise(done => setTimeout(done, delay))
            return statusFailure ? respond({ error: { code: 'isolated_failure', message: '授权读取暂时失败' } }, 502) : respond(status)
          case '/license/free/prepare':
            assert.equal(req.method(), 'POST'); assert.equal(req.postData(), null)
            return respond(proof)
          case '/license/activation/prepare':
            assert.equal(JSON.parse(req.postData()).code, 'isolated-paid-code')
            return respond({ ...proof, purpose: undefined })
          case '/license/activation/commit':
            if (++commitAttempts === 1) return respond({ error: { code: 'license_sync_failed', message: 'DNS 证明尚未生效，请重试' } }, 502)
            status = { ...free }; return respond(status)
          case '/license/sync': status = { ...licensed }; return respond(status)
          case '/license/reminders/dismiss':
            if (++dismissAttempts === 1) return respond({ error: { code: 'isolated_failure', message: '保存失败' } }, 502)
            status = { ...status, reminder_due: false }; return respond(status)
          default: report.errors.push({ state, unexpectedAPI: suffix }); return respond({ error: { message: 'unmocked API' } }, 500)
        }
      })
      await context.route('**/*', async route => {
        const url = route.request().url()
        if (!url.startsWith(origin) && !url.startsWith('data:')) {
          report.errors.push({ state, externalRequest: url }); return route.abort()
        }
        return route.fallback()
      })
      const card = page.getByRole('region', { name: '面板域名授权' })
      async function capture(name, locator = card) {
        state = name
        await locator.scrollIntoViewIfNeeded()
        const dimensions = await page.evaluate(() => ({ viewport: innerWidth, width: document.documentElement.scrollWidth }))
        assert.ok(dimensions.width <= dimensions.viewport + 1, `${name} page overflow: ${JSON.stringify(dimensions)}`)
        const overflow = await locator.evaluate(root => [...root.querySelectorAll('input, button, dd, code')].filter(e => e.getBoundingClientRect().right > innerWidth + 1).map(e => e.tagName + ':' + e.textContent))
        assert.deepEqual(overflow, [], `${name} control overflow`)
        const file = `${viewport.width}-${name}.png`
        if (locator === card) await page.evaluate(() => scrollTo(0, 0))
        await page.screenshot({ path: path.join(output, file), fullPage: locator === card })
        report.screenshots.push(file); report.checks.push({ width: viewport.width, state: name, passed: true, dimensions })
      }
      async function load(value, name) {
        status = value; state = name; await page.goto(`${origin}/userinfo`, { waitUntil: 'networkidle' }); await card.waitFor()
      }
      await load({ ...initial, auto_registration: true }, 'automatic-pending')
      await card.getByText(/正在自动验证免费域名并登记 HT/).waitFor()
      assert.equal(await card.getByRole('button', { name: '登记免费域名' }).count(), 0)
      await capture('automatic-pending')
      status = { ...free }
      await card.getByRole('button', { name: '刷新登记状态' }).click()
      await card.getByRole('heading', { name: '免费域名已验证', exact: true }).waitFor()
      await capture('automatic-active')
      assert.equal(await card.locator('a').count(), 0)
      await card.getByRole('button', { name: '寻求帮助', exact: true }).click()
      await card.getByText('联系方式待补充。', { exact: true }).waitFor()
      await capture('help-placeholder')
      await load({ ...initial }, 'unactivated')
      assert.equal(await card.getByRole('button', { name: '同步授权', exact: true }).isDisabled(), true)
      await capture('unactivated')
      await card.getByRole('button', { name: '登记免费域名' }).click()
      await card.getByText(proof.dns_name, { exact: true }).waitFor()
      assert.equal(await card.locator('input[type=password]').count(), 0)
      await capture('free-proof')
      await card.getByRole('button', { name: '验证 DNS 并开通免费版' }).click()
      await card.getByText('DNS 证明尚未生效，请重试', { exact: true }).waitFor()
      assert.equal(await card.getByText(proof.dns_value, { exact: true }).count(), 1)
      await capture('dns-retry')
      await card.getByRole('button', { name: '验证 DNS 并开通免费版' }).click()
      await card.getByText('免费域名已验证，额度已生效', { exact: true }).waitFor()
      assert.ok((await card.innerText()).includes('10 / 10 条'))
      assert.ok(!(await card.innerText()).includes('永久授权'))
      await capture('free-active')
      await load({ ...free, usage: { device_groups: 2, rules: 11 } }, 'over-quota')
      await card.getByText(/当前已有内容超过免费额度/).waitFor(); await capture('over-quota')
      await load({ ...free, usage: undefined, usage_unavailable: true }, 'usage-unavailable')
      await card.getByText(/当前用量暂未读到/).waitFor(); await capture('usage-unavailable')
      await load({ ...initial }, 'paid-switch')
      await card.getByRole('button', { name: '登记免费域名' }).click()
      await card.getByText(proof.dns_name, { exact: true }).waitFor()
      await card.getByRole('button', { name: '激活授权码', exact: true }).click()
      await card.locator('input[type=password]').fill('isolated-paid-code')
      await card.getByRole('button', { name: '获取域名证明' }).click()
      await card.getByRole('button', { name: '验证 DNS 并激活', exact: true }).waitFor()
      assert.equal(await card.locator('input[type=password]').inputValue(), '')
      await capture('paid-proof')
      await load({ ...initial, status: 'upgrade_required', license_id: 'lic-browser-old' }, 'legacy')
      assert.equal(await card.getByRole('button', { name: '登记免费域名' }).count(), 0)
      await card.getByText('同步后显示', { exact: true }).waitFor(); await capture('legacy')
      await card.getByRole('button', { name: '同步授权', exact: true }).click()
      await card.getByText('授权状态已同步', { exact: true }).waitFor(); await capture('licensed')
      await load({ ...licensed, status: 'expired', management_allowed: false }, 'expired')
      await card.getByText('授权已到期', { exact: true }).waitFor(); await capture('expired')
      await load({ ...licensed, reminder_due: true }, 'reminder')
      const dialog = page.getByRole('alertdialog')
      await dialog.waitFor()
      await dialog.getByRole('button', { name: '今日不再提醒' }).click()
      await dialog.getByRole('alert').waitFor()
      await capture('reminder-error', dialog)
      await dialog.getByRole('button', { name: '今日不再提醒' }).focus()
      await page.keyboard.press('Tab')
      assert.equal(await dialog.getByRole('button', { name: '寻求帮助', exact: true }).evaluate(e => e === document.activeElement), true)
      await page.keyboard.press('Shift+Tab')
      assert.equal(await dialog.getByRole('button', { name: '今日不再提醒' }).evaluate(e => e === document.activeElement), true)
      await page.keyboard.press('Escape')
      await dialog.waitFor({ state: 'detached' }); await capture('reminder-dismissed')
      statusFailure = true
      await load({ ...initial }, 'read-error')
      await card.getByRole('heading', { name: '授权状态读取失败' }).waitFor(); await capture('read-error')
      statusFailure = false
      await card.getByRole('button', { name: '重试', exact: true }).click()
      await card.getByRole('heading', { name: '等待激活', exact: true }).waitFor(); await capture('read-recovered')
      await context.close()
    }
    assert.deepEqual(report.errors, [], 'browser errors')
    report.success = true
  } catch (error) { report.success = false; report.failure = error.stack; throw error }
  finally { await browser.close(); report.finished_at = new Date().toISOString(); await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2)) }
  console.log(JSON.stringify({ success: report.success, checks: report.checks.length, screenshots: report.screenshots.length, errors: report.errors.length, output }))
}
main().catch(error => { console.error(error); process.exitCode = 1 })
