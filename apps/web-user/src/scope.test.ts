import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

import { describe, expect, it } from 'vitest'

const userSurfaceFiles = [
  './layouts/UserShell.vue',
  './pages/HomePage.vue',
  './pages/ProfilePage.vue',
  './pages/ServicesPage.vue',
]

describe('customer portal scope', () => {
  it('does not expose explicitly excluded NY business modules', () => {
    const source = userSurfaceFiles
      .map((path) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8'))
      .join('\n')

    for (const excluded of ['商店', '推送通道', 'Telegram', '订单管理', '套餐管理', '立即续费', '充值', '钱包', '支付', '兑换码管理', '邀请记录', '邀请返利']) {
      expect(source).not.toContain(excluded)
    }
  })
})
