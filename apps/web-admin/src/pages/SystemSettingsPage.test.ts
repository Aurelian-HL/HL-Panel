import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Announcement, PublicSiteInfo, SiteSettings } from '@/api/siteTypes'

const mocked = vi.hoisted(() => ({
  settings: vi.fn(), announcements: vi.fn(), saveSettings: vi.fn(), saveAnnouncement: vi.fn(), publicInfo: vi.fn(),
}))
vi.mock('@/api/site', () => ({ siteApi: mocked }))
vi.mock('@/api/license', () => ({ licenseApi: { status: vi.fn().mockResolvedValue({ items: [] }) } }))
vi.mock('@/api/panelLicense', () => ({ panelLicenseApi: { status: vi.fn().mockResolvedValue({ enabled: false }) } }))
vi.mock('@/api/releases', async original => ({
  ...await original<typeof import('@/api/releases')>(),
  checkVersion: vi.fn().mockResolvedValue({ state: 'up_to_date', current_version: 'test' }),
}))
import SystemSettingsPage from './SystemSettingsPage.vue'
import { toast } from '@/composables/toast'

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
  it('does not call a successful save a failure when the public-info refresh fails', async () => {
    const success = vi.spyOn(toast, 'success'), error = vi.spyOn(toast, 'error')
    mocked.publicInfo.mockRejectedValueOnce(new Error('临时断网'))
    const wrapper = mount(SystemSettingsPage); await flushPromises()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(success).toHaveBeenCalledWith('系统设置已保存')
    expect(error).toHaveBeenCalledWith('设置已保存，页面信息刷新失败', '请刷新页面获取最新站点信息')
    expect(error).not.toHaveBeenCalledWith('系统设置保存失败', expect.anything())
    expect(mocked.saveSettings).toHaveBeenCalledOnce()
    wrapper.unmount(); success.mockRestore(); error.mockRestore()
  })
  it('prevents concurrent saves and shows a refresh failure with retained settings', async () => {
    let resolve!: (value: SiteSettings) => void
    mocked.saveSettings.mockReturnValueOnce(new Promise<SiteSettings>(done => { resolve = done }))
    const wrapper = mount(SystemSettingsPage); await flushPromises()
    await wrapper.get('form').trigger('submit'); await wrapper.get('form').trigger('submit')
    expect(mocked.saveSettings).toHaveBeenCalledOnce()
    resolve({ ...settings, revision: 3 }); await flushPromises()
    mocked.settings.mockRejectedValueOnce(new Error('临时断网'))
    await wrapper.get('.page-heading button').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('刷新失败，保留上次设置')
    expect((wrapper.get('input[maxlength="80"]').element as HTMLInputElement).value).toBe(settings.site_name)
    wrapper.unmount()
  })
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
