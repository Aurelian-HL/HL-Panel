import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ portal: vi.fn() }))
vi.mock('@/api', () => ({ customerApi: { portal: mocked.portal } }))
import HomePage from './HomePage.vue'

describe('HomePage', () => {
  it('renders configured NY-style version, announcement, site and backend information', async () => {
    mocked.portal.mockResolvedValue({
      site_name: '测试站点', panel_version: '20261003',
      announcement: { title: '站点公告', content: '维护信息', updated_at: '2026-10-03T00:00:00Z' },
      site_info: [{ label: '站点说明', value: '公开服务说明' }],
      backend_info: [{ label: '服务状态', value: '运行中' }],
    })
    const wrapper = mount(HomePage, { global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } })
    await flushPromises()

    const text = wrapper.text()
    for (const value of ['欢迎使用', '测试站点 面板版本: 20261003', '站点公告', '维护信息', '站点信息', '后端信息']) {
      expect(text).toContain(value)
    }
    wrapper.unmount()
  })
})
