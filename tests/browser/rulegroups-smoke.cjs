const assert = require('node:assert/strict')
const path = require('node:path')

const { chromium } = require(process.env.PLAYWRIGHT_PATH)

const webURL = process.env.WEB_URL || 'http://127.0.0.1:4175'
const apiURL = process.env.API_URL || 'http://127.0.0.1:8080/api/v1'
const outputDir = process.env.SCREENSHOT_DIR || path.resolve('artifacts/browser')

async function main() {
  const browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH,
    headless: true,
  })
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  const browserErrors = []
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(`console: ${message.text()}`)
  })
  page.on('pageerror', (error) => browserErrors.push(`page: ${error.message}`))
  page.on('requestfailed', (request) => browserErrors.push(`request: ${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`))
  page.on('response', (response) => {
    if (response.status() >= 400) browserErrors.push(`response: ${response.status()} ${response.url()}`)
  })

  try {
    await page.goto(webURL, { waitUntil: 'networkidle' })
    await page.getByLabel('管理员账号').fill('admin')
    await page.getByLabel('密码', { exact: true }).fill('local-test-only')
    await page.getByRole('button', { name: '登录控制台' }).click()
    await page.waitForURL('**/overview')
    const loginStorage = await page.evaluate(() => ({
      token: sessionStorage.getItem('ny_admin_access_token'),
      user: sessionStorage.getItem('ny_admin_user'),
      expiresAt: sessionStorage.getItem('ny_admin_expires_at'),
    }))

    await page.goto(`${webURL}/rule-groups`, { waitUntil: 'networkidle' })
    const addGroupButton = page.getByRole('button', { name: '添加分组' })
    if (await addGroupButton.count() !== 1 || !await addGroupButton.isEnabled()) {
      const currentStorage = await page.evaluate(() => ({
        token: sessionStorage.getItem('ny_admin_access_token'),
        user: sessionStorage.getItem('ny_admin_user'),
        expiresAt: sessionStorage.getItem('ny_admin_expires_at'),
      }))
      throw new Error(`rule-group page unavailable at ${page.url()}; login storage=${JSON.stringify(loginStorage)} current storage=${JSON.stringify(currentStorage)} errors=${JSON.stringify(browserErrors)} body=${await page.locator('body').innerText()}`)
    }
    await addGroupButton.click()
    await page.locator('#rule-group-editor input').fill('浏览器验收分组')
    await page.locator('#rule-group-editor textarea').fill('本地 Playwright 闭环')
    await page.getByRole('button', { name: '确定' }).click()
    await page.getByRole('cell', { name: '浏览器验收分组', exact: true }).waitFor()

    const token = await page.evaluate(() => sessionStorage.getItem('ny_admin_access_token'))
    assert.ok(token, 'login did not persist an access token')
    const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }
    let sequence = 0
    async function request(method, route, body, key) {
      const response = await context.request.fetch(`${apiURL}${route}`, {
        method,
        headers: key ? { ...headers, 'Idempotency-Key': key } : headers,
        data: body,
      })
      const raw = await response.text()
      assert.ok(response.ok(), `${method} ${route} failed: ${response.status()} ${raw}`)
      return raw ? JSON.parse(raw) : undefined
    }
    function key(prefix) {
      sequence += 1
      return `browser-${prefix}-${sequence}`
    }

    const groups = await request('GET', '/rule-groups')
    const ruleGroup = groups.items.find((item) => item.name === '浏览器验收分组')
    assert.ok(ruleGroup, 'created rule group was not returned by the API')
    const device = (await request('POST', '/device-groups', {
      name: '浏览器入口组', kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '本地验收',
    })).group
    await request('PUT', `/group-networks/${device.id}`, {
      connect_host: 'entry.browser.test', port_start: 23000, port_end: 23009,
      direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 0,
    }, key('network'))
    const userGroup = (await request('POST', '/user-groups', {
      name: '浏览器用户组', description: '', allowed_entry_group_ids: [device.id], allowed_exit_group_ids: [], allow_direct: true, revision: 0,
    }, key('user-group'))).user_group
    const customer = (await request('POST', '/customers', {
      username: 'browser-user', display_name: '浏览器验收客户', user_group_id: userGroup.id,
      password: 'local-browser-password', disabled: false, expires_at: null,
      traffic_limit_bytes: 0, max_rules: 3, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, revision: 0,
    }, key('customer'))).customer
    const rule = (await request('POST', '/forwarding-rules', {
      name: '浏览器批量规则', customer_id: customer.id, rule_group_id: ruleGroup.id,
      entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 0,
      targets: [{ host: '127.0.0.1', port: 19090 }], selection_policy: 'round_robin', paused: false,
      description: '本地浏览器验证', revision: 0,
    }, key('rule'))).rule

    await page.screenshot({ path: path.join(outputDir, 'rule-groups-desktop.png'), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'desktop rule-group page overflows horizontally')

    await page.goto(`${webURL}/forward-rules`, { waitUntil: 'networkidle' })
    await page.getByRole('row', { name: /浏览器批量规则/ }).waitFor()
    await page.locator('.business-desktop').getByLabel('选择规则 浏览器批量规则').check()
    await page.locator('.rule-batch-toolbar').getByRole('button', { name: '暂停' }).click()
    await page.getByText('已批量暂停', { exact: true }).waitFor()
    let persisted = (await request('GET', `/forwarding-rules/${rule.id}`)).rule
    assert.equal(persisted.paused, true)
    assert.equal(persisted.revision, 2)

    await page.locator('.business-desktop').getByLabel('选择规则 浏览器批量规则').check()
    await page.locator('.rule-batch-move select').selectOption('')
    await page.locator('.rule-batch-toolbar').getByRole('button', { name: '移动' }).click()
    await page.getByText('已批量移动分组', { exact: true }).waitFor()
    persisted = (await request('GET', `/forwarding-rules/${rule.id}`)).rule
    assert.equal(persisted.rule_group_id, '')
    assert.equal(persisted.revision, 3)

    await page.locator('.business-desktop').getByLabel('选择规则 浏览器批量规则').check()
    await page.locator('.rule-batch-toolbar').getByRole('button', { name: '恢复' }).click()
    await page.getByText('已批量恢复，等待激活', { exact: true }).waitFor()
    persisted = (await request('GET', `/forwarding-rules/${rule.id}`)).rule
    assert.equal(persisted.paused, false)
    assert.equal(persisted.revision, 4)
    await page.screenshot({ path: path.join(outputDir, 'forward-rules-desktop.png'), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'desktop forwarding page overflows horizontally')

    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${webURL}/rule-groups`, { waitUntil: 'networkidle' })
    await page.getByText('浏览器验收分组', { exact: true }).waitFor()
    await page.screenshot({ path: path.join(outputDir, 'rule-groups-mobile.png'), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'mobile rule-group page overflows horizontally')
    await page.goto(`${webURL}/forward-rules`, { waitUntil: 'networkidle' })
    await page.locator('.business-mobile').getByText('浏览器批量规则', { exact: true }).waitFor()
    await page.screenshot({ path: path.join(outputDir, 'forward-rules-mobile.png'), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'mobile forwarding page overflows horizontally')

    assert.deepEqual(browserErrors, [], `browser errors:\n${browserErrors.join('\n')}`)
    process.stdout.write(JSON.stringify({
      rule_group_id: ruleGroup.id,
      forwarding_rule_id: rule.id,
      final_revision: persisted.revision,
      desktop: { width: 1440, height: 900 },
      mobile: { width: 390, height: 844 },
      browser_errors: browserErrors,
    }, null, 2))
  } catch (error) {
    await page.screenshot({ path: path.join(outputDir, 'rulegroups-smoke-failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    await browser.close()
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
