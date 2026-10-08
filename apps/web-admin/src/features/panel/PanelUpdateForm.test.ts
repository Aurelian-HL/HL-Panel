import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { panelUpdateStatus, startPanelUpdate } from '@/api/panelUpdate'
import PanelUpdateForm from './PanelUpdateForm.vue'
vi.mock('@/api/panelUpdate', () => ({ panelUpdateStatus: vi.fn(), startPanelUpdate: vi.fn() }))
afterEach(() => { vi.clearAllMocks(); vi.useRealTimers() })
describe('panel update form', () => {
  it('confirms with a password, clears it and follows the same persisted background task', async () => {
    vi.useFakeTimers()
    const task = { id: '3a6f4f9c-158d-406a-9c14-c293a1e5321b', target_version: 'v0.1.49', state: 'running' as const, phase: 'download', message: '正在下载', created_at: '', updated_at: '' }
    vi.mocked(panelUpdateStatus).mockResolvedValueOnce({ available: true, task: null }).mockResolvedValue({ available: true, task })
    vi.mocked(startPanelUpdate).mockResolvedValue({ available: true, task })
    const wrapper = mount(PanelUpdateForm, { props: { version: 'v0.1.49' } })
    await flushPromises()
    await wrapper.get('input').setValue('current-private-password')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(startPanelUpdate).toHaveBeenCalledWith('v0.1.49', 'current-private-password', expect.stringMatching(/^[0-9a-f-]{36}$/))
    expect(wrapper.text()).not.toContain('current-private-password')
    expect(wrapper.text()).toContain('关闭弹窗不会取消更新')
    vi.mocked(panelUpdateStatus).mockResolvedValue({ available: true, task: { ...task, state: 'succeeded', message: '更新成功' } })
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.text()).toContain('刷新面板')
    await wrapper.setProps({ version: 'v0.1.50' })
    expect(wrapper.find('form').exists()).toBe(true)
    wrapper.unmount()
  })
  it('keeps terminal fallback for an old install with no privileged worker', async () => {
    vi.mocked(panelUpdateStatus).mockResolvedValue({ available: false, task: null, message: '旧安装先执行终端更新' })
    const wrapper = mount(PanelUpdateForm, { props: { version: 'v0.1.49' } })
    await flushPromises()
    expect(wrapper.text()).toContain('旧安装先执行终端更新')
    expect(wrapper.find('form').exists()).toBe(false)
    wrapper.unmount()
  })
})
