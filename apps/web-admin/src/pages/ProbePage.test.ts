import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { completeProbeSample, probeGroups, unavailableProbeSample } from '@/test/fixtures/probe'

const mocked = vi.hoisted(() => ({ deleteNode: vi.fn(), getDeviceGroups: vi.fn(), getDeviceGroupMembers: vi.fn(), updateDeviceGroupMemberWeight: vi.fn(), getGroup: vi.fn(), getInventory: vi.fn() }))
vi.mock('@/api', () => ({ api: { deleteNode: mocked.deleteNode, getDeviceGroups: mocked.getDeviceGroups, getDeviceGroupMembers: mocked.getDeviceGroupMembers, updateDeviceGroupMemberWeight: mocked.updateDeviceGroupMemberWeight } }))
vi.mock('@/api/probe', () => ({ probeApi: { getGroup: mocked.getGroup, getInventory: mocked.getInventory } }))

import ProbePage from './ProbePage.vue'

beforeEach(() => {
  mocked.deleteNode.mockReset().mockResolvedValue({ node_id: 'offline-native', replayed: false })
  localStorage.removeItem('hl_probe_refresh_intervals_v1')
  mocked.getDeviceGroups.mockReset().mockResolvedValue({ items: probeGroups })
  mocked.getGroup.mockReset().mockResolvedValue(completeProbeSample)
  mocked.getInventory.mockReset().mockResolvedValue(completeProbeSample)
  mocked.getDeviceGroupMembers.mockReset().mockResolvedValue({ items: [{ group_id: 'group-gz', node_id: 'node-gz-1', dial_host: '', weight: 10, priority: 0, retired_at: null, created_at: '', updated_at: '2026-10-04T00:00:00Z' }] })
  mocked.updateDeviceGroupMemberWeight.mockReset().mockImplementation(async (_groupId, _nodeId, weight) => ({ member: { group_id: 'group-gz', node_id: 'node-gz-1', dial_host: '', weight, priority: 0, retired_at: null, created_at: '', updated_at: '2026-10-04T00:00:01Z' }, assignments: [], replayed: false }))
})

describe('ProbePage', () => {
  it('bounds membership requests and stops queued work after leaving the page', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: Array.from({ length: 12 }, (_, index) => ({ ...probeGroups[0], id: `group-${index}` })) })
    const finishes: Array<(value: { items: [] }) => void> = []
    mocked.getDeviceGroupMembers.mockImplementation(() => new Promise(resolve => { finishes.push(resolve) }))
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(mocked.getDeviceGroupMembers).toHaveBeenCalledTimes(4)
    finishes[0]!({ items: [] })
    await flushPromises()
    expect(mocked.getDeviceGroupMembers).toHaveBeenCalledTimes(5)
    wrapper.unmount()
    finishes.slice(1).forEach(finish => finish({ items: [] }))
    await flushPromises()
    expect(mocked.getDeviceGroupMembers).toHaveBeenCalledTimes(5)
  })

  it('does not start probe requests when the group list arrives after unmount', async () => {
    let finish!: (value: { items: typeof probeGroups }) => void
    mocked.getDeviceGroups.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mount(ProbePage)
    wrapper.unmount()
    finish({ items: probeGroups })
    await flushPromises()
    expect(mocked.getDeviceGroupMembers).not.toHaveBeenCalled()
    expect(mocked.getInventory).not.toHaveBeenCalled()
  })

  it('counts native HL probes when Nezha is disabled', async () => {
    mocked.getInventory.mockResolvedValue({ upstream_status: 'disabled', items: [
      { node_id: 'native-node', link_status: 'native', source: 'hl', online: true, name: 'HL 节点', sampled_at: new Date().toISOString() },
    ] })
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('在线1')
    expect(wrapper.findAll('.segmented-control button').find((button) => button.text().includes('在线'))?.text()).toContain('1')
    wrapper.unmount()
  })

  it('summarizes real online readings and filters per host', async () => {
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.get('.page-heading p').text()).toContain('HL主机监控')
    expect(wrapper.findAll('.page-heading__actions .segmented-control button').map((button) => button.text().split(' ')[0])).toEqual(['全部', '在线', '离线'])
    expect(wrapper.find('input[placeholder^="搜索机器"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="刷新探针"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('哪吒已上报的机器；未关联 HL 节点的机器也在此显示。')
    expect(mocked.getInventory).toHaveBeenCalledOnce()
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(mocked.getGroup).toHaveBeenCalledWith('group-gz')
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('8.4 Mbps')
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('4.2 Mbps')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(2)
    const offline = wrapper.findAll('.segmented-control button').find((button) => button.text().includes('离线'))!
    await offline.trigger('click')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(1)
    expect(wrapper.get('.probe-table tbody').text()).toContain('122.13.18.216')
    wrapper.unmount()
  })

  it('only offers weight changes for a real member of the selected group', async () => {
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.find('[aria-label^="更改广州电信入口权重"]').exists()).toBe(true)
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).toContain('10')
    expect(wrapper.find('[aria-label^="更改广州联通入口权重"]').exists()).toBe(false)
    await wrapper.get('[aria-label="更改广州电信入口权重，当前 10"]').trigger('click')
    await flushPromises()
    const input = document.body.querySelector<HTMLInputElement>('.modal input[type="number"]')!
    input.value = '0'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    document.body.querySelector<HTMLFormElement>('.modal form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(mocked.updateDeviceGroupMemberWeight).toHaveBeenCalledWith('group-gz', 'node-gz-1', 0, '2026-10-04T00:00:00Z', expect.any(String))
    expect(wrapper.find('[aria-label="更改广州电信入口权重，当前 0"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('groups the full inventory and sums only fresh online samples per group', async () => {
    mocked.getDeviceGroupMembers.mockImplementation(async (groupId: string) => ({ items: groupId === 'group-gz'
      ? [{ group_id: groupId, node_id: 'node-gz-1', dial_host: '', weight: 10, priority: 0, retired_at: null, created_at: '', updated_at: '' }]
      : [{ group_id: groupId, node_id: 'node-gz-1', dial_host: '', weight: 2, priority: 0, retired_at: null, created_at: '', updated_at: '' }] }))
    const wrapper = mount(ProbePage)
    await flushPromises()
    const sections = wrapper.findAll('.probe-section')
    expect(sections).toHaveLength(3)
    expect(sections.map((section) => section.get('.probe-section__name').text())).toEqual(['广州入口', '香港出口', '未入组机器'])
    expect(sections[0]!.get('.probe-section__rate').text()).toContain('↑ 4.2 Mbps')
    expect(sections[0]!.get('.probe-section__rate').text()).toContain('↓ 8.4 Mbps')
    expect(sections[1]!.get('.probe-section__rate').text()).toContain('↑ 4.2 Mbps')
    expect(sections[2]!.get('.probe-section__rate').text()).toContain('未采集')
    expect(sections[0]!.get('.probe-table tbody').text()).toContain('121.14.61.68')
    expect(sections[1]!.get('.probe-table tbody').text()).toContain('121.14.61.68')
    expect(sections[2]!.get('.probe-table tbody').text()).toContain('122.13.18.216')
    wrapper.unmount()
  })

  it('clears old group readings and ignores out-of-order requests on group switch', async () => {
    const wrapper = mount(ProbePage)
    await flushPromises()
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    let resolveOld: (value: typeof completeProbeSample) => void = () => undefined
    mocked.getGroup.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    mocked.getGroup.mockResolvedValueOnce(unavailableProbeSample)
    await wrapper.get('.probe-group-select select').setValue('group-hk')
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).toContain('未关联')
    expect(wrapper.get('.inline-warning').text()).toContain('哪吒服务当前不可用')
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('未采集')
    resolveOld(completeProbeSample)
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).not.toContain('121.14.61.68')
    wrapper.unmount()
  })

  it('keeps previous sample and marks a refresh failure', async () => {
    const wrapper = mount(ProbePage)
    await flushPromises()
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    mocked.getGroup.mockRejectedValueOnce(new Error('探针连接超时'))
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).toContain('121.14.61.68')
    expect(wrapper.get('.inline-warning').text()).toContain('探针连接超时')
    expect(wrapper.get('.probe-table tbody').text()).toContain('数据不可用')
    expect(wrapper.get('.probe-table tbody').text()).not.toContain('8.4 Mbps')
    let resolveRetry: (value: typeof completeProbeSample) => void = () => undefined
    mocked.getGroup.mockImplementationOnce(() => new Promise((resolve) => { resolveRetry = resolve }))
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).toContain('数据不可用')
    resolveRetry(completeProbeSample)
    await flushPromises()
    expect(wrapper.get('.probe-table tbody').text()).toContain('8.4 Mbps')
    wrapper.unmount()
  })

  it('does not aggregate an expired online sample', async () => {
    const expired = { ...completeProbeSample, items: [
      { ...completeProbeSample.items[0]!, sampled_at: new Date(Date.now() - 30_001).toISOString() },
      completeProbeSample.items[1]!,
    ] }
    mocked.getGroup.mockResolvedValue(expired)
    const wrapper = mount(ProbePage)
    await flushPromises()
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('下行速率未采集')
    expect(wrapper.get('.probe-table tbody').text()).toContain('采样过期')
    expect(wrapper.findAll('.segmented-control button')).toHaveLength(3)
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(2)
    wrapper.unmount()
  })

  it('shows unmapped Nezha agents even without HL groups', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: [] })
    mocked.getInventory.mockResolvedValue({ upstream_status: 'ok', items: [
      { node_id: 'nezha:1', link_status: 'unmanaged', nezha_server_id: 1, name: 'Agent 1', online: true, sampled_at: new Date().toISOString(), cpu_percent: 10 },
      { node_id: 'nezha:2', link_status: 'unmanaged', nezha_server_id: 2, name: 'Agent 2', online: true, sampled_at: new Date().toISOString(), cpu_percent: 20 },
    ] })
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(mocked.getGroup).not.toHaveBeenCalled()
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(2)
    expect(wrapper.get('.probe-table tbody').text()).toContain('在线')
    expect(wrapper.get('.probe-table tbody').text()).toContain('在线')
    wrapper.unmount()
  })

  it('does not call an unavailable upstream an empty inventory', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: [] })
    mocked.getInventory.mockResolvedValue({ upstream_status: 'unavailable', items: [] })
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.text()).toContain('哪吒服务当前不可用')
    expect(wrapper.text()).not.toContain('暂无已上报机器')
    wrapper.unmount()
  })

  it('saves foreground and background refresh intervals', async () => {
    const wrapper = mount(ProbePage)
    await flushPromises()
    await wrapper.get('[aria-label="刷新间隔设置"]').trigger('click')
    expect(document.body.textContent).toContain('刷新间隔设置')
    const fields = document.body.querySelectorAll<HTMLInputElement>('.modal input[type="number"]')
    expect(fields).toHaveLength(2)
    fields[0]!.value = '3000'
    fields[0]!.dispatchEvent(new Event('input', { bubbles: true }))
    fields[1]!.value = '12000'
    fields[1]!.dispatchEvent(new Event('input', { bubbles: true }))
    document.body.querySelector<HTMLFormElement>('.modal form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(JSON.parse(localStorage.getItem('hl_probe_refresh_intervals_v1')!)).toEqual({ foreground: 3000, background: 12000 })
    expect(document.body.querySelector('.modal')).toBeNull()
    wrapper.unmount()
  })

  it('polls in the foreground and stops after leaving the page', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const wrapper = mount(ProbePage)
      await flushPromises()
      expect(mocked.getInventory).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(2_000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(4_000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})

it.each(['grouped', 'ungrouped'])('deletes an offline HL probe in the %s section and refreshes inventory', async (placement) => {
  const sample = { upstream_status: 'disabled', items: [{ node_id: 'offline-native', link_status: 'native', source: 'hl', online: false, name: '离线待清理', ipv4: '192.0.2.43' }] }
  mocked.getInventory.mockResolvedValueOnce(sample).mockResolvedValue({ ...sample, items: [] })
  mocked.getDeviceGroupMembers.mockResolvedValue({ items: placement === 'grouped' ? [{ group_id: 'group-gz', node_id: 'offline-native', weight: 100, priority: 0 }] : [] })
  const wrapper = mount(ProbePage)
  await flushPromises()
  expect(wrapper.get('.probe-table tbody').text()).toContain('删除')
  await wrapper.get('[aria-label="删除离线待清理"]').trigger('click')
  await flushPromises()
  document.body.querySelector<HTMLButtonElement>('.modal .button--danger')!.click()
  await flushPromises()
  expect(mocked.deleteNode).toHaveBeenCalledWith('offline-native', expect.any(String))
  expect(wrapper.find('[aria-label="删除离线待清理"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('尚未注册节点')
  wrapper.unmount()
})
