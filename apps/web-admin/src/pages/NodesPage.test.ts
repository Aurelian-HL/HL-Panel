import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { EdgeNode } from '@/api'

const mocked = vi.hoisted(() => ({ deleteNode: vi.fn(), getNodes: vi.fn() }))
vi.mock('@/api', () => ({ api: { deleteNode: mocked.deleteNode, getNodes: mocked.getNodes } }))

import NodesPage from './NodesPage.vue'

const base: EdgeNode = {
  id: 'node-1', name: '广州入口', hostname: 'gz-01', platform: 'linux', architecture: 'amd64',
  agent_version: '1.0.0', boot_id: 'boot-1', engine_versions: { xray: '25.9.11' },
  resources: { logical_cpus: 4, go_max_procs: 4, memory_alloc_bytes: 1048576 },
  status: 'online', capabilities: ['xray', 'tcp'], desired_generation: 2, applied_generation: 2,
  last_apply_status: 'succeeded', last_apply_message: '',
  last_heartbeat_at: '2026-10-03T00:00:00Z', created_at: '2026-10-01T00:00:00Z',
}

beforeEach(() => {
  mocked.deleteNode.mockReset().mockResolvedValue({ node_id: 'node-43', replayed: false })
  mocked.getNodes.mockReset().mockResolvedValue({ items: [base, {
    ...base, id: 'node-2', name: '香港出口', hostname: 'hk-01', status: 'failed',
    desired_generation: 3, applied_generation: 2, last_apply_status: 'failed',
    last_apply_message: '配置校验失败',
  }] })
})

describe('NodesPage', () => {
  it('shows actual status counts and filters the inventory', async () => {
    const wrapper = mount(NodesPage)
    await flushPromises()
    expect(wrapper.get('.node-status-summary').text()).toContain('应用失败 1')
    expect(wrapper.get('.node-status-summary').text()).toContain('心跳在线 2')
    const failedFilter = wrapper.findAll('.segmented-control button').find((button) => button.text().includes('失败'))!
    await failedFilter.trigger('click')
    expect(wrapper.findAll('.node-table tbody tr')).toHaveLength(1)
    expect(wrapper.get('.node-table tbody').text()).toContain('香港出口')
    expect(wrapper.get('.node-table tbody').text()).not.toContain('广州入口')
    wrapper.unmount()
  })

  it('opens current node details and surfaces apply diagnostics', async () => {
    const wrapper = mount(NodesPage, { attachTo: document.body })
    await flushPromises()
    await wrapper.get('[aria-label="查看香港出口详情"]').trigger('click')
    expect(document.body.textContent).toContain('配置校验失败')
    expect(document.body.textContent).toContain('Agent 已分配内存')
    expect(document.body.textContent).toContain('1.0 MiB')
    expect(document.body.textContent).toContain('不代表整机内存占用')
    expect(document.body.textContent).toContain('CPU 使用率、整机内存、磁盘、网络速率和连接数尚未接入')
    expect(document.body.textContent).toContain('以下仅为历史采样')
    wrapper.unmount()
  })

  it('keeps the last successful snapshot after a refresh error', async () => {
    const wrapper = mount(NodesPage)
    await flushPromises()
    mocked.getNodes.mockRejectedValueOnce(new Error('网络超时'))
    await wrapper.get('.page-heading__actions button').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.node-table tbody tr')).toHaveLength(2)
    expect(wrapper.get('.inline-warning').text()).toContain('网络超时')
    wrapper.unmount()
  })

  it('refreshes visible nodes periodically and stops polling after unmount', async () => {
    let refresh: (() => void) | undefined
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval').mockImplementation((callback) => {
      refresh = callback as () => void
      return 42 as ReturnType<typeof setInterval>
    })
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval').mockImplementation(() => undefined)
    const wrapper = mount(NodesPage)
    await flushPromises()
    expect(setIntervalSpy).toHaveBeenCalledWith(expect.any(Function), 30_000)
    expect(mocked.getNodes).toHaveBeenCalledTimes(1)

    refresh?.()
    await flushPromises()
    expect(mocked.getNodes).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    expect(clearIntervalSpy).toHaveBeenCalledWith(42)
    setIntervalSpy.mockRestore()
    clearIntervalSpy.mockRestore()
  })
})

it('deletes an offline node from desktop and mobile lists while retaining online nodes', async () => {
  const offline = { ...base, id: 'node-43', name: '过期节点', status: 'offline' }
  mocked.getNodes.mockResolvedValueOnce({ items: [base, offline] }).mockResolvedValue({ items: [base] })
  const wrapper = mount(NodesPage)
  await flushPromises()
  expect(wrapper.findAll('[aria-label="删除过期节点"]')).toHaveLength(2)
  expect(wrapper.find('[aria-label="删除广州入口"]').exists()).toBe(false)
  await wrapper.get('[aria-label="删除过期节点"]').trigger('click')
  await flushPromises()
  document.body.querySelector<HTMLButtonElement>('.modal .button--danger')!.click()
  await flushPromises()
  expect(mocked.deleteNode).toHaveBeenCalledWith('node-43', expect.any(String))
  expect(wrapper.findAll('.node-table tbody tr')).toHaveLength(1)
  expect(wrapper.findAll('.node-mobile-card')).toHaveLength(1)
  expect(wrapper.text()).not.toContain('过期节点')
  wrapper.unmount()
})
