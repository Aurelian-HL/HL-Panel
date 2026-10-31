// Browser acceptance uses loopback and synthetic rules only.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright')
const origin = process.env.WEB_URL || 'http://127.0.0.1:4196'
const localURL = new URL(origin)
assert.equal(localURL.protocol, 'http:')
assert.ok(['127.0.0.1', 'localhost'].includes(localURL.hostname) && localURL.port && localURL.origin === origin)
const output = path.resolve(process.env.SCREENSHOT_DIR || 'artifacts/forwarding-batch-create-browser')
const settings = { site_name: 'HL-PANEL', panel_title: 'HL 本地验收', theme: 'classic' }
const user = { id: 'adm-local', username: 'admin', created_at: '2026-10-30T00:00:00Z', must_change_password: false }
const device = { id: 'entry-local', name: '美国组', kind: 'ENTRY', member_count: 2, current_generation: 1, selection_policy: 'weighted_round_robin' }
const target = { server_name: 'www.apple.com', destination: 'www.apple.com:443' }
const content = '192.0.2.10:1080:test:local-only\n客户昵称 192.0.2.11:1080:test:local-only'

async function main() {
  await fs.mkdir(output, { recursive: true })
  const report = { synthetic: true, checks: [], screenshots: [], errors: [] }
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH, headless: true })
  try {
    for (const width of [1440, 1024, 390, 320]) {
      const context = await browser.newContext({ viewport: { width, height: 900 } })
      await context.addInitScript(account => {
        sessionStorage.setItem('ny_admin_access_token', 'isolated-batch-token')
        sessionStorage.setItem('ny_admin_user', JSON.stringify(account))
        sessionStorage.setItem('ny_admin_expires_at', '2099-01-01T00:00:00Z')
      }, user)
      let rules = [], probeFailure = false, batchFailure = true, expired = false, finishBatch, batchGate = null
      const batchRequests = []
      const page = await context.newPage()
      page.setDefaultTimeout(10_000)
      page.on('pageerror', error => report.errors.push({ width, error: error.message }))
      page.on('console', message => {
        if (message.type() === 'error' && !/Failed to load resource.*(401|503)/.test(message.text())) report.errors.push({ width, error: message.text() })
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
          case '/system/version': return respond({ current_version: 'local', latest_version: 'local', state: 'up_to_date', attention_required: false })
          case '/auth/me': return respond({ user })
          case '/license/status': return respond({ enabled: true, license_id: 'lic-local', tier: 'licensed', status: 'active', management_allowed: true, domain: 'panel.example.test', expires_at: null })
          case '/device-groups': return respond({ items: [device] })
          case '/group-networks': return respond({ items: [{ group_id: device.id, connect_host: 'entry.example.test', direct_policy: 'OPTIONAL', allow_direct: true, port_start: 10000, port_end: 20000, allowed_exit_group_ids: [], revision: 1 }] })
          case '/rule-groups': return respond({ items: [{ id: 'rgroup-local', name: '默认组', revision: 1 }] })
          case '/forwarding-rules': return respond({ items: rules })
          case '/forwarding-reality-targets': return respond({ items: [target] })
          case '/forwarding-reality-target-probes':
            return respond({ probe: { ...JSON.parse(request.postData()), compatible: !probeFailure, latency_ms: 12, source: 'control_plane', checked_at: '2026-10-30T00:00:00Z' } })
          case '/forwarding-rules/batch-create': {
            assert.equal(request.method(), 'POST')
            const input = JSON.parse(request.postData())
            batchRequests.push({ key: request.headers()['idempotency-key'], input })
            if (batchGate) await batchGate
            if (expired) return respond({ error: { message: '登录已失效' } }, 401)
            if (batchFailure) return respond({ error: { code: 'temporarily_unavailable', message: '本地模拟：保存暂时失败，请重试' } }, 503)
            rules = input.rules.map((item, index) => {
              const { vless_socks5_password, ...publicFields } = item
              return { ...publicFields, id: 'created-' + index, listen_port: 10000 + index, customer_id: '', traffic_used_bytes: 0, revision: 1, status: 'pending_activation' }
            })
            return respond({ result: { created: rules.length, items: rules } }, 201)
          }
          default:
            report.errors.push({ width, unexpectedAPI: url.pathname }); return respond({ error: { message: 'unmocked API' } }, 500)
        }
      })
      async function check(name) {
        const layout = await page.getByRole('dialog').evaluate(root => {
          const rect = root.getBoundingClientRect()
          const overflow = [...root.querySelectorAll('input, textarea, select, button, .batch-preview')].filter(element => {
            const box = element.getBoundingClientRect()
            return box.width > 0 && (box.left < rect.left - 1 || box.right > rect.right + 1 || element.tagName === 'BUTTON' && element.scrollWidth > element.clientWidth + 2)
          }).map(element => element.tagName + ':' + element.className)
          return { left: rect.left, right: rect.right, width: innerWidth, documentWidth: document.documentElement.scrollWidth, overflow }
        })
        assert.ok(layout.documentWidth <= width + 1, `${width} ${name}: document overflow`)
        assert.ok(layout.left >= 0 && layout.right <= width + 1, `${width} ${name}: dialog overflow`)
        assert.deepEqual(layout.overflow, [], `${width} ${name}: control clipping`)
        const screenshot = `${width}-${name}.png`
        await page.screenshot({ path: path.join(output, screenshot), fullPage: true })
        report.screenshots.push(screenshot); report.checks.push({ width, name, passed: true })
      }
      await page.goto(`${origin}/forward-rules`, { waitUntil: 'networkidle' })
      await page.getByRole('button', { name: '批量创建', exact: true }).click()
      const dialog = page.getByRole('dialog', { name: '批量创建规则' })
      const create = dialog.locator('button[type="submit"]')
      await dialog.getByText('Reality 目标已检测：www.apple.com', { exact: true }).waitFor()
      assert.equal(await create.isDisabled(), true)
      await check('empty')
      await dialog.getByLabel('入口设备组').selectOption(device.id)
      await dialog.getByLabel('昵称与 SK5').fill(content)
      assert.deepEqual(await dialog.locator('.batch-row strong').allTextContents(), ['192.0.2.10', '客户昵称'])
      assert.equal((await dialog.locator('.batch-preview').innerText()).includes('local-only'), false)
      assert.equal(await create.isEnabled(), true)
      await check('valid-default-ip')
      await dialog.getByLabel('昵称与 SK5').fill(content + '\n坏行')
      await dialog.getByRole('alert').filter({ hasText: '第 3 行' }).waitFor()
      assert.equal(await create.isDisabled(), true)
      assert.equal(batchRequests.length, 0)
      await check('invalid-row')
      await dialog.getByLabel('昵称与 SK5').fill(content)
      probeFailure = true
      await dialog.getByRole('button', { name: '重新检测', exact: true }).click()
      await dialog.getByRole('status').filter({ hasText: '候选目标均未检测通过' }).waitFor()
      assert.equal(await create.isDisabled(), true)
      await check('probe-failed')
      probeFailure = false
      await dialog.getByRole('button', { name: '重新检测', exact: true }).click()
      await dialog.getByText('Reality 目标已检测：www.apple.com', { exact: true }).waitFor()
      batchGate = new Promise(resolve => { finishBatch = resolve })
      await create.click()
      await dialog.getByRole('button', { name: '正在保存…', exact: true }).waitFor()
      assert.equal(await dialog.getByLabel('昵称与 SK5').isDisabled(), true)
      assert.equal(await dialog.getByRole('button', { name: '取消', exact: true }).isDisabled(), true)
      await page.keyboard.press('Escape')
      assert.equal(await dialog.isVisible(), true)
      await check('saving')
      finishBatch(); batchGate = null
      await dialog.getByRole('alert').filter({ hasText: '保存暂时失败' }).waitFor()
      assert.equal(await dialog.getByLabel('昵称与 SK5').inputValue(), content)
      assert.equal(rules.length, 0)
      await check('save-failed')
      batchFailure = false
      await create.click()
      await dialog.waitFor({ state: 'detached' })
      assert.equal(batchRequests.length, 2)
      assert.ok(batchRequests[0].key)
      assert.equal(batchRequests[0].key, batchRequests[1].key)
      assert.deepEqual(batchRequests[0].input, batchRequests[1].input)
      assert.deepEqual(rules.map(item => item.name), ['192.0.2.10', '客户昵称'])
      assert.equal(await page.locator('.rule-inventory').getByText('192.0.2.10', { exact: true }).count() > 0, true)
      const screenshot = `${width}-saved-list.png`
      await page.screenshot({ path: path.join(output, screenshot), fullPage: true })
      report.screenshots.push(screenshot); report.checks.push({ width, name: 'saved-idempotent-retry', passed: true })
      if (width === 1440) {
        expired = true
        await page.getByRole('button', { name: '批量创建', exact: true }).click()
        await dialog.getByText('Reality 目标已检测：www.apple.com', { exact: true }).waitFor()
        await dialog.getByLabel('入口设备组').selectOption(device.id)
        await dialog.getByLabel('昵称与 SK5').fill(content)
        await create.click()
        await page.waitForURL(url => url.pathname === '/login')
        assert.equal(await dialog.count(), 0)
        report.checks.push({ width, name: 'expired-session', passed: true })
      }
      await context.close()
    }
    assert.deepEqual(report.errors, [], 'unexpected browser errors')
    report.success = true
  } catch (error) { report.success = false; report.failure = error.stack; throw error }
  finally { await browser.close(); await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2)) }
  console.log(JSON.stringify({ success: report.success, checks: report.checks.length, screenshots: report.screenshots.length, errors: report.errors.length, output }))
}
main().catch(error => { console.error(error); process.exitCode = 1 })
