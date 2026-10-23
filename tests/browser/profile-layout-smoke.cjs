// Browser acceptance uses loopback and synthetic accounts only.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright')
const origin = process.env.WEB_URL || 'http://127.0.0.1:4186'
assert.match(origin, /^http:\/\/(127\.0\.0\.1|localhost):\d+$/)
const output = path.resolve(process.env.SCREENSHOT_DIR || 'artifacts/profile-layout-browser')
const settings = { site_name: 'HL-PANEL', panel_title: 'HL 本地界面验收', theme: 'classic' }
const user = { id: 'adm-local', username: 'admin', created_at: '2026-10-20T08:00:00Z', must_change_password: false }
const license = { enabled: true, license_id: 'lic-local', tier: 'licensed', status: 'active', management_allowed: true, domain: 'panel.example.test', expires_at: null, checked_at: '2026-10-24T00:00:00Z' }

async function main() {
  await fs.mkdir(output, { recursive: true })
  const report = { synthetic: true, checks: [], screenshots: [], errors: [] }
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH, headless: true })
  try {
    for (const width of [1920, 1440, 1024, 768, 390, 320]) {
      const context = await browser.newContext({ viewport: { width, height: 1000 } })
      await context.addInitScript(account => {
        sessionStorage.setItem('ny_admin_access_token', 'isolated-profile-token')
        sessionStorage.setItem('ny_admin_user', JSON.stringify(account))
        sessionStorage.setItem('ny_admin_expires_at', '2099-01-01T00:00:00Z')
      }, user)
      let profile = { ...user }, currentLicense = { ...license }, profileError = false
      let finishProfile, finishPassword, passwordRequests = 0
      let profileGate = null, passwordGate = null, passwordFailure = false
      const page = await context.newPage()
      page.on('pageerror', error => report.errors.push({ width, error: error.message }))
      page.on('console', message => {
        if (message.type() === 'error' && !/Failed to load resource.*(400|503)/.test(message.text())) report.errors.push({ width, error: message.text() })
      })
      await context.route('**/*', async route => {
        const request = route.request(), url = new URL(request.url())
        if (url.origin !== origin && url.protocol !== 'data:') {
          report.errors.push({ width, externalRequest: url.origin }); return route.abort()
        }
        if (!url.pathname.startsWith('/api/v1/')) return route.continue()
        const respond = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
        switch (url.pathname.replace('/api/v1', '')) {
          case '/site-settings': return respond({ settings })
          case '/public/site-info': return respond({ settings, announcements: [], platform_version: 'local-verification' })
          case '/announcements': return respond({ items: [] })
          case '/system/version': return respond({ current_version: 'local-verification', latest_version: 'local-verification', state: 'up_to_date', attention_required: false })
          case '/auth/me':
            if (profileGate) await profileGate
            return profileError ? respond({ error: { message: '账号资料暂时不可用' } }, 503) : respond({ user: profile })
          case '/license/status': return respond(currentLicense)
          case '/auth/password':
            assert.equal(request.method(), 'PUT')
            assert.deepEqual(JSON.parse(request.postData()), { current_password: 'old-local-password', new_password: 'new-local-password' })
            passwordRequests++
            if (passwordGate) await passwordGate
            return passwordFailure ? respond({ error: { message: '当前密码错误' } }, 400) : respond({})
          default:
            report.errors.push({ width, unexpectedAPI: url.pathname }); return respond({ error: { message: 'unmocked API' } }, 500)
        }
      })
      async function check(name) {
        const layout = await page.locator('.profile-page').evaluate(root => {
          const cards = [...root.querySelector('.profile-sections').children].map(element => {
            const rect = element.getBoundingClientRect()
            return { x: rect.x, y: rect.y, right: rect.right, bottom: rect.bottom, width: rect.width }
          })
          const overflow = [...root.querySelectorAll('input, button, dd, code, summary')].filter(element => {
            const rect = element.getBoundingClientRect()
            return rect.width > 0 && (rect.left < -1 || rect.right > innerWidth + 1 || element.scrollWidth > element.clientWidth + 2)
          }).map(element => element.tagName + ':' + element.textContent)
          return { width: innerWidth, documentWidth: document.documentElement.scrollWidth, cards, overflow }
        })
        assert.ok(layout.documentWidth <= width + 1, `${width} ${name}: document overflow`)
        assert.deepEqual(layout.overflow, [], `${width} ${name}: control clipping`)
        assert.equal(layout.cards.length, 3)
        for (let index = 1; index < layout.cards.length; index++) {
          assert.ok(Math.abs(layout.cards[index].x - layout.cards[0].x) < 1)
          assert.ok(Math.abs(layout.cards[index].width - layout.cards[0].width) < 1)
          assert.ok(layout.cards[index].y >= layout.cards[index - 1].bottom + 12, `${name}: overlapping cards`)
        }
        const screenshot = `${width}-${name}.png`
        // Reset after focus-driven scrolling so sticky shell chrome is captured
        // at the top of the full-page image rather than across its middle.
        await page.evaluate(() => { window.scrollTo(0, 0) })
        await page.screenshot({ path: path.join(output, screenshot), fullPage: true })
        report.screenshots.push(screenshot); report.checks.push({ width, name, passed: true })
      }
      await page.goto(`${origin}/userinfo`, { waitUntil: 'networkidle' })
      await page.getByRole('region', { name: '账号资料' }).getByText('admin', { exact: true }).waitFor()
      await check('default')
      const fields = page.locator('.profile-password-form input')
      await page.getByRole('button', { name: '显示密码', exact: true }).click()
      assert.deepEqual(await fields.evaluateAll(inputs => inputs.map(input => input.type)), ['text', 'text', 'text'])
      await page.getByRole('button', { name: '隐藏密码', exact: true }).click()
      assert.deepEqual(await fields.evaluateAll(inputs => inputs.map(input => input.type)), ['password', 'password', 'password'])
      await fields.nth(0).fill('old-local-password')
      await fields.nth(1).fill('new-local-password')
      await fields.nth(2).fill('different-password')
      await page.getByRole('button', { name: '修改密码', exact: true }).click()
      await page.getByRole('alert').filter({ hasText: '两次输入的新密码不一致' }).waitFor()
      assert.equal(passwordRequests, 0)
      await check('password-mismatch')
      passwordFailure = true
      await fields.nth(2).fill('new-local-password')
      await page.getByRole('button', { name: '修改密码', exact: true }).click()
      await page.getByRole('alert').filter({ hasText: '当前密码错误' }).waitFor()
      await check('password-rejected')
      profile.must_change_password = true
      await page.reload({ waitUntil: 'networkidle' })
      await page.locator('[aria-label="首次登录修改密码"]').waitFor()
      await check('initial-password')
      profile.must_change_password = false
      profileError = true
      await page.reload({ waitUntil: 'networkidle' })
      await page.getByText('账号资料加载失败', { exact: true }).waitFor()
      await check('profile-error')
      profileError = false
      await page.getByRole('button', { name: '重新加载', exact: true }).click()
      await page.getByText('账号资料加载失败', { exact: true }).waitFor({ state: 'detached' })
      profile.username = '很长的账号名称'.repeat(14)
      currentLicense.domain = 'very-long-panel-name.'.repeat(5) + 'example.test'
      await page.reload({ waitUntil: 'networkidle' })
      await check('long-content')
      profile = { ...user }; currentLicense = { ...license }
      profileGate = new Promise(resolve => { finishProfile = resolve })
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.getByRole('region', { name: '账号资料' }).waitFor()
      await page.getByRole('region', { name: '面板域名授权' }).waitFor()
      await check('pending-profile')
      finishProfile(); profileGate = null
      await page.waitForLoadState('networkidle')
      passwordFailure = false
      passwordGate = new Promise(resolve => { finishPassword = resolve })
      await fields.nth(0).fill('old-local-password')
      await fields.nth(1).fill('new-local-password')
      await fields.nth(2).fill('new-local-password')
      const beforeSubmit = passwordRequests
      await page.getByRole('button', { name: '修改密码', exact: true }).click()
      await page.getByRole('button', { name: '保存中', exact: true }).waitFor()
      assert.equal(await page.getByRole('button', { name: '保存中', exact: true }).isDisabled(), true)
      await check('saving-password')
      finishPassword(); passwordGate = null
      await page.waitForURL(url => url.pathname === '/login')
      assert.equal(passwordRequests, beforeSubmit + 1)
      assert.equal(await page.evaluate(() => sessionStorage.getItem('ny_admin_access_token')), null)
      report.checks.push({ width, name: 'password-success-logout', passed: true })
      await context.close()
    }
    assert.deepEqual(report.errors, [], 'unexpected browser errors')
    report.success = true
  } catch (error) { report.success = false; report.failure = error.stack; throw error }
  finally { await browser.close(); await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2)) }
  console.log(JSON.stringify({ success: report.success, checks: report.checks.length, screenshots: report.screenshots.length, errors: report.errors.length, output }))
}
main().catch(error => { console.error(error); process.exitCode = 1 })
