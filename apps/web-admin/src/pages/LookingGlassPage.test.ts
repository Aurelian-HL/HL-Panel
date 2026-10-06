import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ targets: vi.fn(), run: vi.fn() }))
vi.mock('@/api/diagnostics', () => ({ diagnosticsApi: mocked }))

import LookingGlassPage from './LookingGlassPage.vue'

beforeEach(() => {
  mocked.targets.mockReset().mockResolvedValue([{ id: 'target-1', label: '公共解析器', kind: 'hostname' }])
  mocked.run.mockReset().mockResolvedValue({ target_id: 'target-1', action: 'ping', status: 'ok', checked_at: '2026-10-03T00:00:00Z' })
})

describe('LookingGlassPage', () => {
  it('shows a local-only scope and runs a fixed target', async () => {
    const wrapper = mount(LookingGlassPage)
    await flushPromises()
    expect(wrapper.text()).toContain('控制面本机诊断')
    expect(wrapper.get('select').text()).toContain('公共解析器')
    await wrapper.findAll('.toolbar .button').at(-1)!.trigger('click')
    await flushPromises()
    expect(mocked.run).toHaveBeenCalledWith('target-1', 'ping')
    expect(wrapper.get('.diagnostic-result').text()).toContain('检测成功')
    wrapper.unmount()
  })

  it('renders empty and request-error states', async () => {
    mocked.targets.mockResolvedValueOnce([])
    const empty = mount(LookingGlassPage)
    await flushPromises()
    expect(empty.text()).toContain('暂无诊断目标')
    empty.unmount()

    mocked.run.mockRejectedValueOnce(new Error('诊断暂不可用'))
    const wrapper = mount(LookingGlassPage)
    await flushPromises()
    await wrapper.findAll('.toolbar .button').at(-1)!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('诊断暂不可用')
    wrapper.unmount()
  })

  it('disables DNS for a literal IP and marks a failed result', async () => {
    mocked.targets.mockResolvedValueOnce([{ id: 'target-2', label: '公网地址', kind: 'ip' }])
    mocked.run.mockResolvedValueOnce({ target_id: 'target-2', action: 'ping', status: 'failed', checked_at: '2026-10-03T00:00:00Z' })
    const wrapper = mount(LookingGlassPage)
    await flushPromises()
    expect(wrapper.findAll('.segmented-control button')[1]?.attributes('disabled')).toBeDefined()
    await wrapper.findAll('.toolbar .button').at(-1)!.trigger('click')
    await flushPromises()
    expect(wrapper.get('.diagnostic-result--failed').text()).toContain('检测失败')
    wrapper.unmount()
  })
})
