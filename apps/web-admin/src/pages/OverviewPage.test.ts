import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ getOverview: vi.fn(), loadSite: vi.fn(), controlNode: vi.fn(), getNodeControl: vi.fn() }))

vi.mock('@/api', () => ({ api: { getOverview: mocked.getOverview, controlNode: mocked.controlNode, getNodeControl: mocked.getNodeControl } }))
vi.mock('@/stores/site', () => ({
  siteStore: {
    info: { value: { platform_version: 'v0.1.31', build_time: '2026-10-07T10:00:00Z', announcements: [] } },
    load: mocked.loadSite,
  },
}))

import OverviewPage from './OverviewPage.vue'

const overview = {
  node_count: 2,
  group_count: 1,
  online_node_count: 1,
  syncing_node_count: 1,
  failed_apply_count: 0,
  nodes: [
    {
      id: 'node-1', name: '广州入口', hostname: 'gz-01', platform: 'linux', architecture: 'amd64', agent_version: '1.0.0', boot_id: 'boot-1', engine_versions: { xray: '25.9.11' },
      resources: { host: { cpu_percent: 24, memory_used_bytes: 2 * 1024 ** 3, memory_total_bytes: 8 * 1024 ** 3, swap_used_bytes: 0, swap_total_bytes: 0, disk_used_bytes: 40 * 1024 ** 3, disk_total_bytes: 100 * 1024 ** 3, net_in_speed_bytes_per_second: 1024 ** 2, net_out_speed_bytes_per_second: 512 * 1024, ipv4: '198.51.100.10', uptime_seconds: 90061, tcp_conn_count: 12, udp_conn_count: 3, load_average_1: 0.03, load_average_5: 0.05, load_average_15: 0.08 } },
      status: 'online', capabilities: ['xray'], desired_generation: 2, applied_generation: 2, last_apply_status: 'succeeded', last_apply_message: '配置已应用', last_heartbeat_at: '2026-10-07T10:00:00Z', created_at: '2026-10-01T00:00:00Z',
    },
    {
      id: 'node-2', name: '香港出口', hostname: 'hk-01', platform: 'linux', architecture: 'amd64', agent_version: '1.0.0', boot_id: 'boot-2', engine_versions: {}, resources: {},
      status: 'syncing', capabilities: [], desired_generation: 2, applied_generation: 1, last_apply_status: 'pending', last_apply_message: '', last_heartbeat_at: null, created_at: '2026-10-01T00:00:00Z',
    },
  ],
}

beforeEach(() => {
  mocked.getOverview.mockReset().mockResolvedValue(overview)
  mocked.loadSite.mockReset().mockResolvedValue(undefined)
  mocked.controlNode.mockReset().mockResolvedValue({ node_id: 'node-1', command_id: 'c-1', command: 'restart', status: 'succeeded', message: '引擎已重启', logs: '', updated_at: '2026-10-07T10:00:00Z' })
  mocked.getNodeControl.mockReset().mockResolvedValue(null)
})

describe('OverviewPage', () => {
  it('matches the X-Panel overview blocks and exposes every live metric', async () => {
    const wrapper = mount(OverviewPage)
    await flushPromises()

    expect(wrapper.get('.xpanel-brand h1').text()).toBe('HL-Panel')
    expect(wrapper.get('.xpanel-system').text()).toContain('系统状态')
    expect(wrapper.get('.xpanel-gauges').text()).toContain('24%')
    expect(wrapper.get('.xpanel-gauges').text()).toContain('交换分区')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('Xray 运行状态')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('Xray v25.9.11')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('停止')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('重启')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('版本')
    expect(wrapper.get('.xpanel-logs').text()).toContain('日志')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('总数据')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('连接数')
    expect(wrapper.get('.xpanel-side').text()).toContain('系统正常运行时间')
    expect(wrapper.get('.xpanel-side').text()).toContain('系统负载')
    expect(wrapper.get('.xpanel-side').text()).toContain('整体速度')
    expect(wrapper.get('.xpanel-side').text()).toContain('IP 地址')
    expect(wrapper.find('.overview-action-grid').exists()).toBe(false)
    wrapper.unmount()
  })

  it('sends the selected engine control command through the closed API', async () => {
    const wrapper = mount(OverviewPage)
    await flushPromises()
    const buttons = wrapper.findAll('.xpanel-controls button')
    const restart = buttons[2]
    if (!restart) throw new Error('restart button missing')
    await restart.trigger('click')
    await flushPromises()
    expect(mocked.controlNode).toHaveBeenCalledWith('node-1', 'restart')
  })
})
