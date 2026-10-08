import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ publicInfo: vi.fn() }))
vi.mock('@/api/site', () => ({ siteApi: { publicInfo: mocked.publicInfo } }))

beforeEach(() => {
  vi.resetModules()
  sessionStorage.clear()
  mocked.publicInfo.mockReset()
})

describe('siteStore brand', () => {
  it('coalesces simultaneous forced refreshes while a request is pending', async () => {
    let resolve!: (value: unknown) => void
    mocked.publicInfo.mockReturnValue(new Promise((done) => { resolve = done }))
    const { siteStore } = await import('./site')
    const pending = [siteStore.load(), siteStore.load(true), siteStore.load(true)]
    expect(mocked.publicInfo).toHaveBeenCalledTimes(1)
    resolve({ settings: { site_name: 'HL-panel', panel_title: 'HL-panel' }, announcements: [] })
    await Promise.all(pending)
    expect(siteStore.siteName.value).toBe('HL-panel')
  })
  it('uses the last configured brand immediately after a refresh, then updates it from the public API', async () => {
    sessionStorage.setItem('hl_panel_brand_v1', JSON.stringify({ site_name: '已配置站名', panel_title: '已配置标题' }))
    mocked.publicInfo.mockResolvedValue({ settings: { site_name: '新站名', panel_title: '新标题' }, announcements: [] })
    const { siteStore } = await import('./site')
    expect(siteStore.siteName.value).toBe('已配置站名')
    expect(siteStore.panelTitle.value).toBe('已配置标题')
    await siteStore.load()
    expect(siteStore.siteName.value).toBe('新站名')
    expect(siteStore.panelTitle.value).toBe('新标题')
    expect(JSON.parse(sessionStorage.getItem('hl_panel_brand_v1') || '{}')).toMatchObject({ panel_title: '新标题' })
  })

  it('falls back to HL-panel when there is no configuration available yet', async () => {
    const { siteStore } = await import('./site')
    expect(siteStore.siteName.value).toBe('HL-panel')
    expect(siteStore.panelTitle.value).toBe('HL-panel')
  })
})
