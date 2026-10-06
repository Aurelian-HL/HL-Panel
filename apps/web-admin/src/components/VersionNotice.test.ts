import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { checkVersion, type VersionStatus } from '@/api/releases'
import VersionNotice from './VersionNotice.vue'

vi.mock('@/api/releases', async (original) => ({
  ...await original<typeof import('@/api/releases')>(),
  checkVersion: vi.fn(),
}))

const check = vi.mocked(checkVersion)
function status(behind: number): VersionStatus {
  return { current_version: 'v0.1.5', latest_version: 'v0.1.8', versions_behind: behind, versions_behind_at_least: false,
    state: behind >= 3 ? 'attention_required' : 'update_available', attention_required: behind >= 3,
    can_defer: behind <= 2, checked_at: '', message: '发现新的正式版本' }
}
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => { localStorage.clear(); check.mockReset() })
afterEach(() => { wrapper?.unmount(); wrapper = undefined; localStorage.clear() })

describe('version reminder policy', () => {
  it.each([1, 2])('can defer %i releases for the same version only', async (behind) => {
    check.mockResolvedValue(status(behind))
    wrapper = mount(VersionNotice)
    await flushPromises()
    const button = wrapper.findAll('button').find((b) => b.text() === '下次更新')!
    await button.trigger('click')
    expect(wrapper.find('.version-banner').exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem('hl-panel-version-reminder')!).version).toBe('v0.1.8')
    wrapper.unmount()
    wrapper = mount(VersionNotice)
    await flushPromises()
    expect(wrapper.find('.version-banner').exists()).toBe(false)
    check.mockResolvedValue({ ...status(behind), latest_version: 'v0.1.9' })
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(wrapper.find('.version-banner').exists()).toBe(true)
  })
  it('cannot hide an urgent reminder using saved deferral or inconsistent can_defer', async () => {
    localStorage.setItem('hl-panel-version-reminder', JSON.stringify({ version: 'v0.1.8', until: Date.now() + 86400000 }))
    check.mockResolvedValue({ ...status(3), can_defer: true })
    wrapper = mount(VersionNotice)
    await flushPromises()
    expect(wrapper.find('.version-banner--urgent').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('下次更新')
    expect(wrapper.get('a').attributes('href')).toBe('https://github.com/Aurelian-HL/HL-Panel/releases')
    await wrapper.findAll('button').find((b) => b.text() === '保留数据更新')!.trigger('click')
    expect(document.body.textContent).toContain('update.sh | bash -s -- --version v0.1.8')
  })
  it('shows a failed GitHub check rather than claiming current', async () => {
    check.mockResolvedValue({ ...status(0), state: 'check_failed', latest_version: '', message: '版本尚未核实' })
    wrapper = mount(VersionNotice)
    await flushPromises()
    expect(wrapper.get('.version-error').text()).toContain('尚未核实')
    expect(wrapper.find('.version-banner').exists()).toBe(false)
  })
})
