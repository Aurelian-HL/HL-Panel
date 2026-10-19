import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { NodeUpdateStatus, NodeUpdateTask } from '@/api/nodeUpdates'

const api = vi.hoisted(() => ({ nodeUpdateStatus: vi.fn(), startNodeUpdate: vi.fn(), cancelNodeUpdate: vi.fn() }))
vi.mock('@/api/nodeUpdates', () => api)
import NodeUpdateDialog from './NodeUpdateDialog.vue'

const initial: NodeUpdateStatus = { available: true, current_version: 'v0.1.122', panel_version: 'v0.1.123', versions: ['v0.1.123', 'v0.1.122'], message: '切换会中断连接', task: null }
const queued: NodeUpdateTask = { id: 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa', node_id: 'node-1', target_version: 'v0.1.123', state: 'queued', phase: 'queued', message: '等待领取', updated_at: '' }
const wrappers: ReturnType<typeof mount>[] = []
function open() {
  const wrapper = mount(NodeUpdateDialog, { props: { nodeId: 'node-1' }, global: { stubs: { Teleport: true } } })
  wrappers.push(wrapper)
  return wrapper
}
beforeEach(() => {
  vi.useFakeTimers()
  sessionStorage.clear()
  vi.resetAllMocks()
  api.nodeUpdateStatus.mockImplementation(async () => structuredClone(initial))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers() })

it('shows the exact tagged bootstrap for an old node and does not offer submission', async () => {
  api.nodeUpdateStatus.mockResolvedValue({ ...initial, available: false })
  const w = open(); await flushPromises()
  expect(w.get('pre').text()).toContain('/v0.1.123/update-node.sh')
  expect(w.get('pre').text()).toContain('--version v0.1.123')
  expect(w.find('.button--primary').exists()).toBe(false)
  expect(w.text()).toContain('节点令牌')
})

it('reuses the task id after an ambiguous submission, then reconciles the receipt', async () => {
  api.startNodeUpdate.mockRejectedValueOnce(new Error('连接超时')).mockResolvedValueOnce(queued)
  const w = open(); await flushPromises()
  await w.get('.button--primary').trigger('click'); await flushPromises()
  expect(w.text()).toContain('连接超时')
  const firstKey = api.startNodeUpdate.mock.calls[0]![2]
  await w.get('.button--primary').trigger('click'); await flushPromises()
  expect(api.startNodeUpdate.mock.calls[1]![2]).toBe(firstKey)
  expect(sessionStorage.getItem('hl-node-update:node-1')).toBeNull()
})

it('only permits cancelling an unclaimed task, with active polling and no stale read error', async () => {
  api.nodeUpdateStatus.mockRejectedValueOnce(new Error('读取失败')).mockResolvedValue({ ...initial, task: queued })
  api.cancelNodeUpdate.mockResolvedValue({ ...queued, state: 'cancelled' })
  const w = open(); await flushPromises()
  expect(w.text()).toContain('读取失败')
  await vi.advanceTimersByTimeAsync(15000); await flushPromises()
  expect(w.text()).not.toContain('读取失败')
  expect(w.get('.button--primary').attributes('disabled')).toBeDefined()
  await w.findAll('button').find(b => b.text() === '取消未领取任务')!.trigger('click'); await flushPromises()
  expect(api.cancelNodeUpdate).toHaveBeenCalledWith('node-1', queued.id)
  api.nodeUpdateStatus.mockResolvedValue({ ...initial, task: { ...queued, state: 'uncertain' } })
  await vi.advanceTimersByTimeAsync(3000); await flushPromises()
  expect(w.text()).toContain('结果待核实')
  expect(w.text()).not.toContain('取消未领取任务')
})

it('supports same-version repair and stops polling when closed', async () => {
  api.nodeUpdateStatus.mockResolvedValue({ ...initial, current_version: 'v0.1.123' })
  const w = open(); await flushPromises()
  expect(w.get('.button--primary').text()).toBe('重新安装此版本')
  const count = api.nodeUpdateStatus.mock.calls.length
  w.unmount(); wrappers.pop()
  await vi.advanceTimersByTimeAsync(30000)
  expect(api.nodeUpdateStatus).toHaveBeenCalledTimes(count)
})
