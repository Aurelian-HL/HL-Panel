import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Announcement, PublicSiteInfo, SiteSettings } from '@/api/siteTypes'

const mocked = vi.hoisted(() => ({
  settings: vi.fn(), announcements: vi.fn(), saveSettings: vi.fn(), saveAnnouncement: vi.fn(), publicInfo: vi.fn(),
}))
vi.mock('@/api/site', () => ({ siteApi: mocked }))
import SystemSettingsPage from './SystemSettingsPage.vue'

const settings: SiteSettings = {
  id: 'global', site_name: '鸿乐线路', panel_title: '线路管理面板', public_description: '站点说明',
  support_url: 'https://support.example.test', theme: 'classic', background_image_url: '', revision: 2,
  updated_at: '2026-10-03T00:00:00Z',
}
const announcement: Announcement = {
  id: 'ann-1', title: '维护通知', content: '今晚维护', level: 'maintenance', enabled: true,
  starts_at: null, ends_at: null, sort_order: 10, revision: 1,
  created_at: '2026-10-03T00:00:00Z', updated_at: '2026-10-03T00:00:00Z',
}
const publicInfo: PublicSiteInfo = { settings, announcements: [announcement], platform_version: 'test', build_time: null }

beforeEach(() => {
  mocked.settings.mockReset().mockResolvedValue(settings)
  mocked.announcements.mockReset().mockResolvedValue([announcement])
  mocked.saveSettings.mockReset().mockResolvedValue({ ...settings, revision: 3 })
  mocked.publicInfo.mockReset().mockResolvedValue(publicInfo)
})

describe('SystemSettingsPage', () => {
  it('loads durable settings and saves a complete replacement', async () => {
    const wrapper = mount(SystemSettingsPage)
    await flushPromises()
    expect(wrapper.text()).toContain('维护通知')
    await wrapper.get('input[maxlength="80"]').setValue('新站点名')
    await wrapper.get('.settings-form').trigger('submit')
    await flushPromises()
    expect(mocked.saveSettings).toHaveBeenCalledOnce()
    expect(mocked.saveSettings.mock.calls[0]?.[0]).toMatchObject({ site_name: '新站点名', revision: 2 })
    expect(mocked.publicInfo).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not expose excluded commerce or push settings', async () => {
    const wrapper = mount(SystemSettingsPage)
    await flushPromises()
    expect(wrapper.text()).not.toMatch(/订单|套餐|兑换码|推送通道|邀请记录/)
    wrapper.unmount()
  })
})
