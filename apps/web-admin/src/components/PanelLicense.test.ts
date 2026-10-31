import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PanelLicenseCard from './PanelLicenseCard.vue'
import PanelLicenseReminder from './PanelLicenseReminder.vue'
import { panelLicenseApi, type PanelLicenseStatus } from '@/api/panelLicense'
import { panelLicenseStore as store } from '@/stores/panelLicense'

vi.mock('@/api/panelLicense', () => ({ panelLicenseApi: { status: vi.fn(), sync: vi.fn(), dismiss: vi.fn(), prepare: vi.fn(), commit: vi.fn(), registerFree: vi.fn() } }))
vi.mock('@/stores/auth', async () => {
  const { computed, ref } = await import('vue')
  const user = ref({ id: 'admin-test' })
  return { authStore: { user, isAuthenticated: computed(() => Boolean(user.value)) } }
})
const sample: PanelLicenseStatus = { enabled: true, tier: 'licensed', limits: { device_groups: 0, rules: 0 }, license_id: 'lic-test', domain: 'panel.example.test', status: 'active', management_allowed: true, expires_at: '2026-10-22T08:00:00Z', reminder_due: true, renewal_url: 'https://ht.hongle.work/', checked_at: '2026-10-20T08:00:00Z' }
beforeEach(() => { vi.clearAllMocks(); store.clear(); vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample }) })

describe('Panel license UI', () => {
  it('offers a contact placeholder without navigating to the issuer', async () => {
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.find('a').exists()).toBe(false)
    const help = wrapper.findAll('button').find(b => b.text() === '寻求帮助')!
    expect(help.attributes('aria-expanded')).toBe('false')
    await help.trigger('click')
    expect(wrapper.text()).toContain('联系方式待补充')
    expect(help.attributes('aria-expanded')).toBe('true')
    await help.trigger('click')
    expect(wrapper.text()).not.toContain('联系方式待补充')
    wrapper.unmount()
  })
  it('shows automatic registration pending and refreshes without requiring DNS or a code', async () => {
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, status: 'unactivated', tier: undefined, license_id: undefined, auto_registration: true, management_allowed: false })
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.text()).toContain('正在自动验证免费域名并登记 HT')
    expect(wrapper.text()).toContain('公网 80 端口可访问')
    expect(wrapper.text()).not.toContain('登记免费域名')
    expect(wrapper.text()).not.toContain('请准备该域名的 DNS 管理权限')
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, tier: 'free', limits: { device_groups: 1, rules: 10 }, expires_at: null })
    await wrapper.findAll('button').find(b => b.text() === '刷新登记状态')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('免费域名已验证')
    expect(panelLicenseApi.prepare).not.toHaveBeenCalled(); expect(panelLicenseApi.registerFree).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('keeps legacy signed identities restricted until explicit entitlements are synchronized', async () => {
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, status: 'upgrade_required', tier: undefined, limits: undefined, expires_at: null, management_allowed: false, reminder_due: false })
    vi.mocked(panelLicenseApi.sync).mockResolvedValue(sample)
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.text()).toContain('旧授权待同步'); expect(wrapper.text()).toContain('同步后显示')
    expect(wrapper.text()).not.toContain('永久授权'); expect(wrapper.text()).not.toContain('登记免费域名')
    await wrapper.findAll('button').find(b => b.text() === '同步授权')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('授权有效'); wrapper.unmount()
  })
  it('registers free without a code and keeps DNS proof after a failed verification', async () => {
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, tier: undefined, limits: undefined, license_id: undefined, status: 'unactivated', management_allowed: false, reminder_due: false })
    vi.mocked(panelLicenseApi.registerFree).mockResolvedValue({ purpose: 'free-registration', dns_name: '_hl-license.panel.example.test', dns_value: 'hl-license-v2=free', expires_at: '2026-10-20T08:10:00Z' })
    const free = { ...sample, tier: 'free' as const, limits: { device_groups: 1, rules: 10 }, usage: { device_groups: 1, rules: 10 }, term: { kind: 'perpetual' as const }, expires_at: null, reminder_due: false }
    vi.mocked(panelLicenseApi.commit).mockRejectedValueOnce(new Error('DNS 证明尚未生效')).mockResolvedValueOnce(free)
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.findAll('button').find(b => b.text() === '同步授权')!.attributes('disabled')).toBeDefined()
    await wrapper.findAll('button').find(b => b.text() === '登记免费域名')!.trigger('click'); await flushPromises()
    expect(panelLicenseApi.registerFree).toHaveBeenCalledOnce(); expect(panelLicenseApi.prepare).not.toHaveBeenCalled()
    expect(wrapper.find('input[type=password]').exists()).toBe(false)
    await wrapper.findAll('button').find(b => b.text() === '验证 DNS 并开通免费版')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('DNS 证明尚未生效'); expect(wrapper.text()).toContain('hl-license-v2=free')
    await wrapper.findAll('button').find(b => b.text() === '验证 DNS 并开通免费版')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('免费域名已验证，额度已生效'); expect(wrapper.text()).toContain('10 / 10 条')
    expect(wrapper.text()).toContain('停用规则也计数'); expect(wrapper.text()).not.toContain('永久授权'); wrapper.unmount()
  })
  it('allows changing from an unfinished free proof to paid activation', async () => {
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, tier: undefined, license_id: undefined, status: 'unactivated', management_allowed: false })
    vi.mocked(panelLicenseApi.registerFree).mockResolvedValue({ purpose: 'free-registration', dns_name: '_hl-license.panel.example.test', dns_value: 'hl-license-v2=free', expires_at: '2026-10-20T08:10:00Z' })
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '登记免费域名')!.trigger('click'); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '激活授权码')!.trigger('click'); await flushPromises()
    expect(wrapper.find('input[type=password]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('验证 DNS 并开通免费版'); wrapper.unmount()
  })
  it('shows existing over-limit content and unavailable usage without granting unlimited quota', async () => {
    vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample, tier: 'free', limits: { device_groups: 1, rules: 10 }, usage: { device_groups: 2, rules: 11 }, expires_at: null, reminder_due: false })
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.text()).toContain('2 / 1 个'); expect(wrapper.text()).toContain('11 / 10 条')
    expect(wrapper.text()).toContain('可维护和删除')
    store.status.value = { ...store.status.value!, usage: undefined, usage_unavailable: true }; await flushPromises()
    expect(wrapper.text()).toContain('当前用量暂未读到'); expect(wrapper.text()).toContain('— / 10 条'); wrapper.unmount()
  })
  it('displays explicit domain, expiry, restrictions and perpetual status without local issuance', async () => {
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    expect(wrapper.text()).toContain('panel.example.test')
    expect(wrapper.text()).toContain('授权有效')
    expect(wrapper.text()).not.toContain('生成授权码')
    store.status.value = { ...sample, status: 'expired', management_allowed: false }
    await flushPromises()
    expect(wrapper.text()).toContain('新增、修改和删除受限')
    expect(wrapper.text()).toContain('现有转发、订阅和节点运行继续保留')
    store.status.value = { ...sample, expires_at: null, reminder_due: false, term: { kind: 'perpetual' } }
    await flushPromises(); expect(wrapper.text()).toContain('永久授权'); wrapper.unmount()
  })
  it('prepares a DNS proof, clears the secret input and commits verified activation', async () => {
    vi.mocked(panelLicenseApi.prepare).mockResolvedValue({ dns_name: '_hl-license.panel.example.test', dns_value: 'hl-license-v2=test', expires_at: '2026-10-20T08:10:00Z' })
    vi.mocked(panelLicenseApi.commit).mockResolvedValue(sample)
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '激活授权码')!.trigger('click')
    await wrapper.get('input[type=password]').setValue('hl-test-secret-code')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(panelLicenseApi.prepare).toHaveBeenCalledWith('hl-test-secret-code')
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).toContain('_hl-license.panel.example.test')
    await wrapper.findAll('button').find(b => b.text() === '验证 DNS 并激活')!.trigger('click'); await flushPromises()
    expect(panelLicenseApi.commit).toHaveBeenCalledOnce(); expect(wrapper.text()).toContain('授权已激活'); wrapper.unmount()
  })
  it('does not claim a throttled or failed sync succeeded', async () => {
    vi.mocked(panelLicenseApi.sync).mockRejectedValue(new Error('授权同步过于频繁，请在 10 秒后重试'))
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '同步授权')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('10 秒后重试'); expect(wrapper.text()).not.toContain('授权状态已同步'); wrapper.unmount()
  })
  it('persists daily dismissal and retains the reminder after save failure', async () => {
    vi.mocked(panelLicenseApi.dismiss).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ ...sample, reminder_due: false })
    const wrapper = mount(PanelLicenseReminder, { attachTo: document.body }); await flushPromises()
    expect(wrapper.get('[role=alertdialog]').attributes('aria-modal')).toBe('true')
    const dismiss = wrapper.get('button[autofocus]')
    expect(wrapper.find('a').exists()).toBe(false)
    expect(document.activeElement).toBe(dismiss.element)
    await dismiss.trigger('click'); await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('提醒设置未保存')
    expect(wrapper.find('[role=alertdialog]').exists()).toBe(true)
    await dismiss.trigger('click'); await flushPromises()
    expect(wrapper.find('[role=alertdialog]').exists()).toBe(false); wrapper.unmount()
  })
  it('keeps a newly applied redemption when an older status request completes', async () => {
    let resolve!: (s: PanelLicenseStatus) => void
    vi.mocked(panelLicenseApi.status).mockReturnValueOnce(new Promise(done => { resolve = done }))
    const pending = store.refresh()
    expect(store.apply(sample, store.generation())).toBe(true)
    resolve({ ...sample, status: 'expired', management_allowed: false }); await pending
    expect(store.status.value).toEqual(sample)
  })
  it('ignores an older status failure after a successful redemption', async () => {
    let reject!: (error: Error) => void
    vi.mocked(panelLicenseApi.status).mockReturnValueOnce(new Promise((_done, fail) => { reject = fail }))
    const pending = store.refresh()
    store.apply(sample, store.generation())
    reject(new Error('stale request failed')); await pending
    expect(store.status.value).toEqual(sample)
    expect(store.error.value).toBe('')
  })
  it('starts a fresh read after redemption without an old response clearing that read', async () => {
    let resolveOld!: (s: PanelLicenseStatus) => void
    let resolveNew!: (s: PanelLicenseStatus) => void
    vi.mocked(panelLicenseApi.status)
      .mockReturnValueOnce(new Promise(done => { resolveOld = done }))
      .mockReturnValueOnce(new Promise(done => { resolveNew = done }))
    const oldRead = store.refresh()
    store.apply(sample, store.generation())
    const newRead = store.refresh()
    const callsAfterApply = vi.mocked(panelLicenseApi.status).mock.calls.length
    resolveOld({ ...sample, status: 'expired' }); await oldRead
    const duplicateRead = store.refresh()
    const callsAfterOldRead = vi.mocked(panelLicenseApi.status).mock.calls.length
    resolveNew({ ...sample, reminder_due: false }); await Promise.all([newRead, duplicateRead])
    expect(callsAfterApply).toBe(2)
    expect(callsAfterOldRead).toBe(2)
    expect(store.status.value?.reminder_due).toBe(false)
  })
  it('discards a response from the previous administrator session', async () => {
    let resolve!: (s: PanelLicenseStatus) => void
    vi.mocked(panelLicenseApi.status).mockReturnValueOnce(new Promise(done => { resolve = done }))
    const pending = store.refresh(); store.clear(); resolve(sample); await pending
    expect(store.status.value).toBeNull()
    await store.refresh(); expect(store.status.value?.domain).toBe(sample.domain)
  })
  it('discards an in-flight management action after the administrator session changes', async () => {
    let resolve!: (s: PanelLicenseStatus) => void
    vi.mocked(panelLicenseApi.sync).mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mount(PanelLicenseCard); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '同步授权')!.trigger('click')
    store.clear(); resolve(sample); await flushPromises()
    expect(store.status.value).toBeNull()
    expect(wrapper.text()).not.toContain('授权状态已同步'); wrapper.unmount()
  })
  it('discards an in-flight dismissal after the administrator session changes', async () => {
    let resolve!: (s: PanelLicenseStatus) => void
    vi.mocked(panelLicenseApi.dismiss).mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mount(PanelLicenseReminder); await flushPromises()
    await wrapper.get('button[autofocus]').trigger('click'); store.clear()
    resolve({ ...sample, reminder_due: false }); await flushPromises()
    expect(store.status.value).toBeNull(); wrapper.unmount()
  })
})
