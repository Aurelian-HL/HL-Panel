import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PanelLicenseCard from './PanelLicenseCard.vue'
import PanelLicenseReminder from './PanelLicenseReminder.vue'
import { panelLicenseApi, type PanelLicenseStatus } from '@/api/panelLicense'
import { panelLicenseStore as store } from '@/stores/panelLicense'

vi.mock('@/api/panelLicense', () => ({ panelLicenseApi: { status: vi.fn(), sync: vi.fn(), dismiss: vi.fn(), prepare: vi.fn(), commit: vi.fn() } }))
vi.mock('@/stores/auth', async () => {
  const { computed, ref } = await import('vue')
  const user = ref({ id: 'admin-test' })
  return { authStore: { user, isAuthenticated: computed(() => Boolean(user.value)) } }
})
const sample: PanelLicenseStatus = { enabled: true, domain: 'panel.example.test', status: 'active', management_allowed: true, expires_at: '2026-10-22T08:00:00Z', reminder_due: true, renewal_url: 'https://ht.hongle.work/', checked_at: '2026-10-20T08:00:00Z' }
beforeEach(() => { vi.clearAllMocks(); store.clear(); vi.mocked(panelLicenseApi.status).mockResolvedValue({ ...sample }) })

describe('Panel license UI', () => {
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
    expect(document.activeElement).toBe(wrapper.get('button').element)
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('提醒设置未保存')
    expect(wrapper.find('[role=alertdialog]').exists()).toBe(true)
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.find('[role=alertdialog]').exists()).toBe(false); wrapper.unmount()
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
    await wrapper.get('button').trigger('click'); store.clear()
    resolve({ ...sample, reminder_due: false }); await flushPromises()
    expect(store.status.value).toBeNull(); wrapper.unmount()
  })
})
