import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { completeProbeSample, probeGroups, unavailableProbeSample } from '@/test/fixtures/probe'

const mocked = vi.hoisted(() => ({ deleteNode: vi.fn(), getDeviceGroups: vi.fn(), getDeviceGroupMembers: vi.fn(), updateDeviceGroupMemberWeight: vi.fn(), getGroup: vi.fn(), getInventory: vi.fn() }))
vi.mock('@/api', () => ({ api: { deleteNode: mocked.deleteNode, getDeviceGroups: mocked.getDeviceGroups, getDeviceGroupMembers: mocked.getDeviceGroupMembers, updateDeviceGroupMemberWeight: mocked.updateDeviceGroupMemberWeight } }))
vi.mock('@/api/probe', () => ({ probeApi: { getGroup: mocked.getGroup, getInventory: mocked.getInventory } }))

import ProbePage from './ProbePage.vue'
import { probeRemarksStorageKey } from '@/features/probe/remarks'
import { probeMemberOrderStorageKey } from '@/features/probe/memberOrder'
import ProbeMembers from '@/features/probe/ProbeMembers.vue'

beforeEach(() => {
  mocked.deleteNode.mockReset().mockResolvedValue({ node_id: 'offline-native', replayed: false })
  localStorage.removeItem('hl_probe_refresh_intervals_v1')
  localStorage.removeItem(probeRemarksStorageKey)
  localStorage.removeItem(probeMemberOrderStorageKey)
  mocked.getDeviceGroups.mockReset().mockResolvedValue({ items: probeGroups })
  mocked.getGroup.mockReset().mockResolvedValue(completeProbeSample)
  mocked.getInventory.mockReset().mockResolvedValue(completeProbeSample)
  mocked.getDeviceGroupMembers.mockReset().mockResolvedValue({ items: [{ group_id: 'group-gz', node_id: 'node-gz-1', dial_host: '', weight: 10, priority: 0, retired_at: null, created_at: '', updated_at: '2026-10-04T00:00:00Z' }] })
  mocked.updateDeviceGroupMemberWeight.mockReset().mockImplementation(async (_groupId, _nodeId, weight) => ({ member: { group_id: 'group-gz', node_id: 'node-gz-1', dial_host: '', weight, priority: 0, retired_at: null, created_at: '', updated_at: '2026-10-04T00:00:01Z' }, assignments: [], replayed: false }))
})

describe('ProbePage', () => {
  function sortableInventory() {
    const items = Array.from({ length: 5 }, (_, index) => ({ ...completeProbeSample.items[0]!, node_id: `sort-${index + 1}`, name: `机器${index + 1}`, ipv4: `192.0.2.${index + 1}`, sampled_at: new Date().toISOString() }))
    const membership = items.map((item, index) => ({ group_id: index < 4 ? 'group-gz' : 'group-hk', node_id: item.node_id, dial_host: '', weight: 10, priority: 0, retired_at: null, created_at: '', updated_at: '' }))
    mocked.getInventory.mockResolvedValue({ upstream_status: 'ok', items })
    mocked.getGroup.mockImplementation(async groupId => ({ upstream_status: 'ok', items: items.filter(item => membership.some(member => member.group_id === groupId && member.node_id === item.node_id)) }))
    mocked.getDeviceGroupMembers.mockImplementation(async groupId => ({ items: membership.filter(member => member.group_id === groupId) }))
    return items
  }

  it('inserts the fourth machine first and third second, preserving display order across refreshes and group changes without server writes', async () => {
    const items = sortableInventory()
    const original = JSON.stringify(items)
    let wrapper = mount(ProbePage)
    await flushPromises()
    const ids = () => wrapper.findAll('.probe-section')[0]!.findAll('.probe-table tbody tr').map(row => row.attributes('data-probe-sort-id'))
    let rows = wrapper.findAll('.probe-section')[0]!.findAll('.probe-table tbody tr')
    await rows[3]!.get('.probe-sort-handle').trigger('keydown', { key: 'Home' })
    expect(ids()).toEqual(['sort-4', 'sort-1', 'sort-2', 'sort-3'])
    rows = wrapper.findAll('.probe-section')[0]!.findAll('.probe-table tbody tr')
    await rows[2]!.get('.probe-sort-handle').trigger('keydown', { key: 'ArrowUp' })
    const expected = ['sort-4', 'sort-2', 'sort-1', 'sort-3']
    expect(ids()).toEqual(expected)
    expect(wrapper.findAll('.probe-section')[0]!.findAll('.probe-mobile-item').map(row => row.attributes('data-probe-sort-id'))).toEqual(expected)
    expect(JSON.parse(localStorage.getItem(probeMemberOrderStorageKey)!)['group-gz']).toEqual(expected)
    window.dispatchEvent(new Event('online'))
    await flushPromises()
    expect(ids()).toEqual(expected)
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(wrapper.findAll('.probe-table tbody tr').map(row => row.attributes('data-probe-sort-id'))).toEqual(expected)
    wrapper.unmount()
    wrapper = mount(ProbePage)
    await flushPromises()
    expect(ids()).toEqual(expected)
    expect(wrapper.findAll('.probe-section')[1]!.findAll('.probe-table tbody tr').map(row => row.attributes('data-probe-sort-id'))).toEqual(['sort-5'])
    expect(JSON.stringify(items)).toBe(original)
    expect(mocked.updateDeviceGroupMemberWeight).not.toHaveBeenCalled()
    expect(mocked.deleteNode).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps the current order and shows an error when local persistence fails', async () => {
    sortableInventory()
    const wrapper = mount(ProbePage)
    await flushPromises()
    const storage = vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    try {
      await wrapper.findAll('.probe-sort-handle')[3]!.trigger('keydown', { key: 'Home' })
      expect(wrapper.get('[role=alert]').text()).toContain('未能保存排序')
      expect(wrapper.findAll('.probe-table tbody tr').map(row => row.attributes('data-probe-sort-id'))).toEqual(['sort-1', 'sort-2', 'sort-3', 'sort-4', 'sort-5'])
      expect(localStorage.getItem(probeMemberOrderStorageKey)).toBeNull()
    } finally { storage.mockRestore(); wrapper.unmount() }
  })

  it('rejects moves outside the current group and synchronizes local preferences between tabs', async () => {
    sortableInventory()
    const wrapper = mount(ProbePage)
    await flushPromises()
    wrapper.findComponent(ProbeMembers).vm.$emit('reorder', { nodeId: 'sort-4', targetId: 'sort-5' })
    await flushPromises()
    expect(localStorage.getItem(probeMemberOrderStorageKey)).toBeNull()
    localStorage.setItem(probeMemberOrderStorageKey, JSON.stringify({ 'group-gz': ['sort-4', 'sort-3', 'sort-2', 'sort-1'] }))
    window.dispatchEvent(new StorageEvent('storage', { key: probeMemberOrderStorageKey }))
    await flushPromises()
    expect(wrapper.findAll('.probe-section')[0]!.findAll('.probe-table tbody tr').map(row => row.attributes('data-probe-sort-id'))).toEqual(['sort-4', 'sort-3', 'sort-2', 'sort-1'])
    expect(mocked.updateDeviceGroupMemberWeight).not.toHaveBeenCalled()
    expect(mocked.deleteNode).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('persists display-only remarks across group changes and remounts, then restores IP when cleared', async () => {
    const originalSample = JSON.stringify(completeProbeSample)
    const options = { global: { stubs: { teleport: true } } }
    let wrapper = mount(ProbePage, options)
    await flushPromises()
    await wrapper.get('.probe-remark-button').trigger('click')
    await wrapper.get('input[placeholder="例如：香港主力 · 01"]').setValue('  香港主力  ')
    await wrapper.get('.modal form').trigger('submit')
    expect(wrapper.get('.probe-address .probe-host-label').text()).toBe('香港主力')
    expect(wrapper.get('.probe-mobile-item header').text()).toContain('香港主力')
    expect(wrapper.get('.probe-address .probe-host-label span').attributes('title')).toContain(completeProbeSample.items[0]!.ipv4)
    expect(wrapper.find('.probe-address small').exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem(probeRemarksStorageKey)!)).toEqual({ 'node-gz-1': '香港主力' })
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(wrapper.get('.probe-address .probe-host-label').text()).toBe('香港主力')
    wrapper.unmount()
    wrapper = mount(ProbePage, options)
    await flushPromises()
    expect(wrapper.get('.probe-address .probe-host-label').text()).toBe('香港主力')
    await wrapper.get('[aria-label="查看广州电信入口监测详情"]').trigger('click')
    expect(wrapper.get('.probe-details__facts').text()).toContain(completeProbeSample.items[0]!.ipv4)
    await wrapper.get('[aria-label="关闭对话框"]').trigger('click')
    await wrapper.get('.probe-remark-button').trigger('click')
    await wrapper.get('.modal input').setValue('')
    await wrapper.get('.modal form').trigger('submit')
    expect(wrapper.get('.probe-address .probe-host-label').text()).toBe(completeProbeSample.items[0]!.ipv4)
    expect(JSON.parse(localStorage.getItem(probeRemarksStorageKey)!)).toEqual({})
    expect(JSON.stringify(completeProbeSample)).toBe(originalSample)
    expect(mocked.updateDeviceGroupMemberWeight).not.toHaveBeenCalled()
    expect(mocked.deleteNode).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('cancels remark edits and keeps the previous display when browser storage rejects a save', async () => {
    localStorage.setItem(probeRemarksStorageKey, JSON.stringify({ 'node-gz-1': '原备注' }))
    const wrapper = mount(ProbePage, { global: { stubs: { teleport: true } } })
    await flushPromises()
    await wrapper.get('.probe-remark-button').trigger('click')
    await wrapper.get('.modal input').setValue('已取消')
    await wrapper.findAll('.modal button').find(button => button.text() === '取消')!.trigger('click')
    expect(wrapper.get('.probe-address .probe-host-label').text()).toBe('原备注')
    await wrapper.get('.probe-remark-button').trigger('click')
    await wrapper.get('.modal input').setValue('不能保存')
    const storage = vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    try {
      await wrapper.get('.modal form').trigger('submit')
      expect(wrapper.get('[role="alert"]').text()).toContain('未能保存备注')
      expect(wrapper.get('.probe-address .probe-host-label').text()).toBe('原备注')
      expect(wrapper.get<HTMLInputElement>('.modal input').element.value).toBe('不能保存')
    } finally {
      storage.mockRestore()
      wrapper.unmount()
    }
  })

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

  it('loads metrics independently of groups and cancels them on unmount', async () => {
    let finish!: (value: { items: typeof probeGroups }) => void
    mocked.getDeviceGroups.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mount(ProbePage)
    wrapper.unmount()
    finish({ items: probeGroups })
    await flushPromises()
    expect(mocked.getDeviceGroupMembers).not.toHaveBeenCalled()
    expect(mocked.getInventory).toHaveBeenCalledTimes(1)
    expect((mocked.getInventory.mock.calls[0]![0] as AbortSignal).aborted).toBe(true)
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
    expect(wrapper.findAll('.probe-pagination__filters button').map((button) => button.text().split(' ')[0])).toEqual(['全部', '在线', '离线'])
    expect(wrapper.find('input[placeholder^="搜索机器"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="刷新探针"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('哪吒已上报的机器；未关联 HL 节点的机器也在此显示。')
    expect(mocked.getInventory).toHaveBeenCalledOnce()
    await wrapper.get('.probe-group-select select').setValue('group-gz')
    await flushPromises()
    expect(mocked.getGroup).toHaveBeenCalledWith('group-gz', expect.any(AbortSignal))
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('8.4 Mbps')
    expect(wrapper.get('.probe-overview__metrics').text()).toContain('4.2 Mbps')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(2)
    const offline = wrapper.findAll('.probe-pagination__filters button').find((button) => button.text().includes('离线'))!
    await offline.trigger('click')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(1)
    expect(wrapper.get('.probe-table tbody').text()).toContain('122.13.18.216')
    wrapper.unmount()
  })

  it('counts every non-online status in offline and filters the same set', async () => {
    const fresh = new Date().toISOString()
    mocked.getDeviceGroups.mockResolvedValue({ items: [] })
    mocked.getInventory.mockResolvedValue({ upstream_status: 'ok', items: [
      { ...completeProbeSample.items[0]!, node_id: 'online', name: '在线机器', sampled_at: fresh, online: true },
      { ...completeProbeSample.items[0]!, node_id: 'offline', name: '明确离线', sampled_at: fresh, online: false },
      { ...completeProbeSample.items[0]!, node_id: 'expired', name: '采样过期', sampled_at: '2026-10-03T23:00:00Z', online: true },
      { ...completeProbeSample.items[0]!, node_id: 'uncollected', name: '未采集', sampled_at: undefined, online: undefined },
      { ...completeProbeSample.items[0]!, node_id: 'unlinked', name: '未关联', link_status: 'unlinked', sampled_at: fresh, online: true },
    ] })
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.findAll('.probe-pagination__filters button').map((button) => button.text())).toEqual(['全部 5', '在线 1', '离线 4'])
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(5)
    await wrapper.findAll('.probe-pagination__filters button')[2]!.trigger('click')
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 1-4，共 4 台')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(4)
    expect(wrapper.get('.probe-table tbody').text()).not.toContain('在线机器')
    wrapper.unmount()
  })

  it('paginates inventory, changes page size, and normalizes the page after filtering', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: [] })
    const fresh = new Date().toISOString()
    const items = Array.from({ length: 25 }, (_, index) => ({
      ...completeProbeSample.items[0]!,
      node_id: `page-node-${index + 1}`,
      name: `分页机器 ${index + 1}`,
      ipv4: `192.0.2.${index + 1}`,
      online: index >= 22 ? false : true,
      sampled_at: fresh,
    }))
    mocked.getInventory.mockResolvedValue({ upstream_status: 'ok', items })
    const wrapper = mount(ProbePage)
    await flushPromises()
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 1-20，共 25 台')
    expect(wrapper.get('.probe-pagination__buttons').text()).toContain('第 1 / 2 页')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(20)
    await wrapper.findAll('.probe-pagination__buttons button')[1]!.trigger('click')
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 21-25，共 25 台')
    expect(wrapper.get('.probe-pagination__buttons').text()).toContain('第 2 / 2 页')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(5)
    await wrapper.findAll('.probe-pagination__filters button')[2]!.trigger('click')
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 1-3，共 3 台')
    expect(wrapper.get('.probe-pagination__buttons').text()).toContain('第 1 / 1 页')
    expect(wrapper.findAll('.probe-table tbody tr')).toHaveLength(3)
    await wrapper.get('.probe-pagination__size select').setValue('50')
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 1-3，共 3 台')
    await wrapper.findAll('.probe-pagination__filters button')[0]!.trigger('click')
    expect(wrapper.get('.probe-pagination__summary').text()).toContain('显示 1-25，共 25 台')
    expect(wrapper.get('.probe-pagination__buttons').text()).toContain('第 1 / 1 页')
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
    expect(wrapper.findAll('.probe-pagination__filters button')).toHaveLength(3)
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

  it('hides refresh controls and ignores previously stored slower intervals', async () => {
    localStorage.setItem('hl_probe_refresh_intervals_v1', JSON.stringify({ foreground: 30000, background: 12000 }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = mount(ProbePage)
    try {
      await flushPromises()
      expect(wrapper.find('.probe-live-status').exists()).toBe(false)
      expect(wrapper.find('[aria-label="立即刷新探针"]').exists()).toBe(false)
      expect(wrapper.find('[aria-label="刷新间隔设置"]').exists()).toBe(false)
      expect(wrapper.text()).not.toContain('自动刷新')
      await vi.advanceTimersByTimeAsync(1000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
    } finally { wrapper.unmount(); vi.useRealTimers() }
  })

  it('polls in the foreground and stops after leaving the page', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const wrapper = mount(ProbePage)
      await flushPromises()
      expect(mocked.getInventory).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1_000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(4_000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('renders new heartbeat readings without overlapping slow requests and cancels on exit', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = mount(ProbePage)
    try {
      await flushPromises()
      let finish!: (value: typeof completeProbeSample) => void
      mocked.getInventory.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
      await vi.advanceTimersByTimeAsync(1_000)
      const signal = mocked.getInventory.mock.calls[1]![0] as AbortSignal
      await vi.advanceTimersByTimeAsync(6_000)
      document.dispatchEvent(new Event('visibilitychange'))
      await flushPromises()
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
      finish({ ...completeProbeSample, items: [{ ...completeProbeSample.items[0]!, net_in_speed_bytes_per_second: 2_000_000, sampled_at: new Date().toISOString() }] })
      await flushPromises()
      expect(wrapper.get('.probe-overview__metrics').text()).toContain('16.0 Mbps')
      expect(wrapper.text()).not.toContain('哪吒连接未配置')
      wrapper.unmount()
      expect(signal.aborted).toBe(true)
      await vi.advanceTimersByTimeAsync(20_000)
      expect(mocked.getInventory).toHaveBeenCalledTimes(2)
    } finally { wrapper.unmount(); vi.useRealTimers() }
  })

  it('automatically reconciles group changes while retaining membership after a temporary error', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = mount(ProbePage)
    try {
      await flushPromises()
      expect(wrapper.get('[aria-label="广州入口探针"]').text()).toContain('121.14.61.68')
      mocked.getDeviceGroupMembers.mockImplementation(async (groupId: string) => ({ items: groupId === 'group-hk' ? [{ group_id: 'group-hk', node_id: 'node-gz-1', weight: 10, priority: 0 }] : [] }))
      await vi.advanceTimersByTimeAsync(15_000)
      await flushPromises()
      expect(wrapper.find('[aria-label="广州入口探针"]').exists()).toBe(false)
      expect(wrapper.get('[aria-label="香港出口探针"]').text()).toContain('121.14.61.68')
      mocked.getDeviceGroupMembers.mockRejectedValue(new Error('暂时无法读取成员'))
      await vi.advanceTimersByTimeAsync(15_000)
      await flushPromises()
      expect(wrapper.get('.probe-table tbody').text()).toContain('121.14.61.68')
      expect(wrapper.text()).toContain('暂时无法读取成员')
    } finally { wrapper.unmount(); vi.useRealTimers() }
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
  if (placement === 'ungrouped') expect(mocked.deleteNode).toHaveBeenCalledWith('offline-native', expect.any(String), 'unassigned')
  else expect(mocked.deleteNode).toHaveBeenCalledWith('offline-native', expect.any(String))
  expect(wrapper.find('[aria-label="删除离线待清理"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('尚未注册节点')
  wrapper.unmount()
})

it('deletes an online retired probe from the unassigned section using the guarded scope', async () => {
  const sample = { upstream_status: 'disabled', items: [{ node_id: 'retired-native', link_status: 'native', source: 'hl', online: true, sampled_at: new Date().toISOString(), name: '退役待清理' }] }
  mocked.getInventory.mockResolvedValueOnce(sample).mockResolvedValue({ ...sample, items: [] })
  mocked.getDeviceGroupMembers.mockResolvedValue({ items: [{ group_id: 'group-gz', node_id: 'retired-native', retired_at: new Date().toISOString(), weight: 100, priority: 0 }] })
  const wrapper = mount(ProbePage)
  await flushPromises()
  expect(wrapper.get('[aria-label="未入组机器探针"]').text()).toContain('在线')
  await wrapper.get('[aria-label="删除退役待清理"]').trigger('click')
  await flushPromises()
  expect(document.body.textContent).toContain('删除未入组机器')
  document.body.querySelector<HTMLButtonElement>('.modal .button--danger')!.click()
  await flushPromises()
  expect(mocked.deleteNode).toHaveBeenCalledWith('retired-native', expect.any(String), 'unassigned')
  expect(wrapper.find('[aria-label="删除退役待清理"]').exists()).toBe(false)
  wrapper.unmount()
})

it('keeps polling after deleting a node while a metrics request is pending', async () => {
  vi.useFakeTimers()
  const sample = { upstream_status: 'disabled', items: [{ node_id: 'offline-native', link_status: 'native', source: 'hl', online: false, name: '离线待清理' }] }
  mocked.getDeviceGroupMembers.mockResolvedValue({ items: [] })
  mocked.getInventory.mockResolvedValueOnce(sample)
  const wrapper = mount(ProbePage)
  try {
    await flushPromises()
    let finishOld: (value: unknown) => void = () => undefined
    mocked.getInventory.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    await vi.advanceTimersByTimeAsync(1000)
    const oldSignal = mocked.getInventory.mock.calls[1]![0] as AbortSignal
    expect(oldSignal.aborted).toBe(false)
    mocked.getInventory.mockResolvedValue({ ...sample, items: [] })
    await wrapper.get('[aria-label="删除离线待清理"]').trigger('click')
    document.body.querySelector<HTMLButtonElement>('.modal .button--danger')!.click()
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    expect(wrapper.text()).toContain('尚未注册节点')
    finishOld(sample)
    await flushPromises()
    expect(wrapper.find('[aria-label="删除离线待清理"]').exists()).toBe(false)
    const calls = mocked.getInventory.mock.calls.length
    await vi.advanceTimersByTimeAsync(1000)
    expect(mocked.getInventory.mock.calls.length).toBeGreaterThan(calls)
  } finally { wrapper.unmount(); vi.useRealTimers() }
})
