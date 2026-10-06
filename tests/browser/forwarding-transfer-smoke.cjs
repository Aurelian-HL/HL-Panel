const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')

const { chromium } = require(process.env.PLAYWRIGHT_PATH)

const webURL = process.env.WEB_URL || 'http://127.0.0.1:4175'
const apiURL = process.env.API_URL || 'http://127.0.0.1:8080/api/v1'
const outputDir = process.env.SCREENSHOT_DIR || path.resolve('artifacts/browser')

async function main() {
  await fs.mkdir(outputDir, { recursive: true })
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH, headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, acceptDownloads: true })
  const page = await context.newPage()
  const browserErrors = []
  page.on('console', (message) => { if (message.type() === 'error') browserErrors.push(`console: ${message.text()}`) })
  page.on('pageerror', (error) => browserErrors.push(`page: ${error.message}`))
  page.on('requestfailed', (request) => browserErrors.push(`request: ${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`))
  page.on('response', (response) => { if (response.status() >= 400) browserErrors.push(`response: ${response.status()} ${response.url()}`) })

  try {
    await page.goto(webURL, { waitUntil: 'networkidle' })
    await page.getByLabel('管理员账号').fill('admin')
    await page.getByLabel('密码', { exact: true }).fill('local-test-only')
    await page.getByRole('button', { name: '登录控制台' }).click()
    await page.waitForFunction(() => Boolean(sessionStorage.getItem('ny_admin_access_token')))
    await page.waitForURL((url) => url.pathname === '/overview')
    const token = await page.evaluate(() => sessionStorage.getItem('ny_admin_access_token'))
    assert.ok(token)
    const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }
    let sequence = 0
    async function request(method, route, body, key) {
      const response = await context.request.fetch(`${apiURL}${route}`, { method, headers: key ? { ...headers, 'Idempotency-Key': key } : headers, data: body })
      const raw = await response.text()
      assert.ok(response.ok(), `${method} ${route} failed: ${response.status()} ${raw}`)
      return raw ? JSON.parse(raw) : undefined
    }
    const key = (name) => `transfer-browser-${name}-${++sequence}`
    const entry = (await request('POST', '/device-groups', { name: '导入验收入口组', kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '' })).group
    await request('PUT', `/group-networks/${entry.id}`, { connect_host: 'entry.import.test', port_start: 24000, port_end: 24009, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 0 }, key('network'))
    const userGroup = (await request('POST', '/user-groups', { name: '导入验收用户组', description: '', allowed_entry_group_ids: [entry.id], allowed_exit_group_ids: [], allow_direct: true, revision: 0 }, key('user-group'))).user_group
    const customer = (await request('POST', '/customers', { username: 'transfer-browser', display_name: '导入验收客户', user_group_id: userGroup.id, password: 'local-browser-password', disabled: false, expires_at: null, traffic_limit_bytes: 0, max_rules: 10, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, revision: 0 }, key('customer'))).customer

    await page.goto(`${webURL}/forward-rules`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: '导入', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: '导入转发规则' })
    await dialog.getByLabel('文件格式').selectOption('ny_text')
    await dialog.getByLabel('导入内容').fill('自动端口##first.example.test#443\n固定端口#24001#second.example.test#8443')
    await dialog.getByLabel('客户').selectOption(customer.id)
    await dialog.getByLabel('入口转发组').selectOption(entry.id)
    await dialog.getByRole('button', { name: '预览并检查' }).click()
    await dialog.getByText('共 2 条，2 条可导入，0 条错误').waitFor()
    assert.equal(await dialog.getByRole('button', { name: '确认导入 2 条' }).isEnabled(), true)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true)
    await page.screenshot({ path: path.join(outputDir, 'forwarding-transfer-desktop.png'), fullPage: true })
    await dialog.getByRole('button', { name: '确认导入 2 条' }).click()
    await page.getByText('规则导入完成', { exact: true }).waitFor()
    const rules = await request('GET', '/forwarding-rules')
    assert.equal(rules.items.length, 2)
    assert.deepEqual(rules.items.map((item) => item.listen_port).sort((a, b) => a - b), [24000, 24001])

    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: '导出', exact: true }).click()
    const download = await downloadPromise
    assert.match(download.suggestedFilename(), /^forwarding-rules-\d{8}\.json$/)
    const downloaded = JSON.parse(await fs.readFile(await download.path(), 'utf8'))
    assert.equal(downloaded.schema_version, 'nyvp.forwarding-rules/v1')
    assert.equal(downloaded.rules.length, 2)

    await page.waitForFunction(() => !document.querySelector('.toast'))
    await page.setViewportSize({ width: 390, height: 844 })
    await page.getByRole('button', { name: '导入', exact: true }).click()
    const mobileDialog = page.getByRole('dialog', { name: '导入转发规则' })
    await mobileDialog.getByLabel('文件格式').selectOption('ny_text')
    await mobileDialog.getByLabel('导入内容').fill('格式错误#只有两列')
    await mobileDialog.getByLabel('客户').selectOption(customer.id)
    await mobileDialog.getByLabel('入口转发组').selectOption(entry.id)
    await mobileDialog.getByRole('button', { name: '预览并检查' }).click()
    await mobileDialog.getByText('必须使用 名称#监听端口#目标地址#目标端口 四列格式').waitFor()
    assert.equal(await mobileDialog.getByRole('button', { name: '确认导入 1 条' }).isDisabled(), true)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true)
    await page.screenshot({ path: path.join(outputDir, 'forwarding-transfer-mobile.png') })

    assert.deepEqual(browserErrors, [], `browser errors:\n${browserErrors.join('\n')}`)
    process.stdout.write(JSON.stringify({ imported: rules.items.length, desktop: [1440, 900], mobile: [390, 844], browser_errors: browserErrors }, null, 2))
  } catch (error) {
    await page.screenshot({ path: path.join(outputDir, 'forwarding-transfer-failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    await browser.close()
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1 })
