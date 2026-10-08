import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ getOverview: vi.fn(), loadSite: vi.fn(), controlPanel: vi.fn(), getPanelControl: vi.fn() }))

vi.mock('@/api/panelLogs', () => ({ getPanelLogs: vi.fn().mockResolvedValue({ items: [{ time: '2026-10-08T00:00:00Z', level: 'INFO', message: '面板真实日志' }], persistent: true }) }))
vi.mock('@/api/panelUpdate', () => ({ panelUpdateStatus: vi.fn().mockResolvedValue({ available: true, task: null }), startPanelUpdate: vi.fn() }))
vi.mock('@/api/releases', () => ({ checkVersion: vi.fn().mockResolvedValue({ current_version: 'v0.1.31', versions: [{ tag: 'v0.1.31', current: true, can_update: false }] }), repositoryUrl: 'https://github.com/Aurelian-HL/HL-Panel', updateCommand: '' }))

vi.mock('@/api', () => ({ api: { getOverview: mocked.getOverview, controlPanel: mocked.controlPanel, getPanelControl: mocked.getPanelControl } }))
vi.mock('@/stores/site', () => ({
  siteStore: {
    info: { value: { platform_version: 'v0.1.31', build_time: '2026-10-07T10:00:00Z', announcements: [] } },
    load: mocked.loadSite,
  },
}))

import OverviewPage from './OverviewPage.vue'

const overview = {
  node_count: 3,
  group_count: 1,
  online_node_count: 2,
  syncing_node_count: 1,
  failed_apply_count: 0,
  panel: {
    status: 'online', version: 'v0.1.31', started_at: '2026-10-03T10:00:00Z', log: '面板服务运行中',
    resources: { cpu_percent: 7, logical_cpus: 4, memory_used_bytes: 3 * 1024 ** 3, memory_total_bytes: 8 * 1024 ** 3, swap_used_bytes: 512 * 1024 ** 2, swap_total_bytes: 2 * 1024 ** 3, disk_used_bytes: 50 * 1024 ** 3, disk_total_bytes: 100 * 1024 ** 3, net_in_transfer_bytes: 9 * 1024 ** 3, net_out_transfer_bytes: 7 * 1024 ** 3, uptime_seconds: 432000, load_average_1: 0.42, load_average_5: 0.37, load_average_15: 0.28, ipv4: '203.0.113.8', ipv6: '2001:db8::8' },
  },
  nodes: [
    {
      id: 'node-1', name: '广州入口', hostname: 'gz-01', platform: 'linux', architecture: 'amd64', agent_version: '1.0.0', boot_id: 'boot-1', engine_versions: { xray: '25.9.11' },
      resources: { host: { cpu_percent: 24, memory_used_bytes: 2 * 1024 ** 3, memory_total_bytes: 8 * 1024 ** 3, swap_used_bytes: 0, swap_total_bytes: 0, disk_used_bytes: 40 * 1024 ** 3, disk_total_bytes: 100 * 1024 ** 3, net_in_transfer_bytes: 4 * 1024 ** 3, net_out_transfer_bytes: 2 * 1024 ** 3, net_in_speed_bytes_per_second: 1024 ** 2, net_out_speed_bytes_per_second: 512 * 1024, ipv4: '198.51.100.10', uptime_seconds: 90061, tcp_conn_count: 12, udp_conn_count: 3, load_average_1: 0.03, load_average_5: 0.05, load_average_15: 0.08 } },
      status: 'online', capabilities: ['xray'], desired_generation: 2, applied_generation: 2, last_apply_status: 'succeeded', last_apply_message: '配置已应用', last_heartbeat_at: '2026-10-07T10:00:00Z', created_at: '2026-10-01T00:00:00Z',
    },
    {
      id: 'node-2', name: '香港出口', hostname: 'hk-01', platform: 'linux', architecture: 'amd64', agent_version: '1.0.0', boot_id: 'boot-2', engine_versions: {}, resources: { host: { net_in_transfer_bytes: 8 * 1024 ** 3, net_out_transfer_bytes: 6 * 1024 ** 3, net_in_speed_bytes_per_second: 3 * 1024 ** 2, net_out_speed_bytes_per_second: 1024 ** 2, tcp_conn_count: 40, udp_conn_count: 9 } },
      status: 'online', capabilities: [], desired_generation: 2, applied_generation: 2, last_apply_status: 'succeeded', last_apply_message: '', last_heartbeat_at: '2026-10-07T10:00:00Z', created_at: '2026-10-01T00:00:00Z',
    },
    {
      id: 'node-3', name: '离线节点', hostname: 'off-01', platform: 'linux', architecture: 'amd64', agent_version: '1.0.0', boot_id: 'boot-3', engine_versions: {}, resources: { host: { net_in_transfer_bytes: 100 * 1024 ** 3, net_out_transfer_bytes: 100 * 1024 ** 3, net_in_speed_bytes_per_second: 20 * 1024 ** 2, net_out_speed_bytes_per_second: 20 * 1024 ** 2, tcp_conn_count: 1000, udp_conn_count: 1000 } },
      status: 'offline', capabilities: [], desired_generation: 1, applied_generation: 1, last_apply_status: 'succeeded', last_apply_message: '', last_heartbeat_at: null, created_at: '2026-10-01T00:00:00Z',
    },
  ],
}

beforeEach(() => {
  mocked.getOverview.mockReset().mockResolvedValue(overview)
  mocked.loadSite.mockReset().mockResolvedValue(undefined)
  mocked.controlPanel.mockReset().mockResolvedValue({ command_id: 'c-1', command: 'restart', status: 'succeeded', message: '面板已重启', logs: '', updated_at: '2026-10-07T10:00:00Z' })
  mocked.getPanelControl.mockReset().mockResolvedValue(null)
})

describe('OverviewPage', () => {
  it.each([
    ['collected', '未配置'],
    ['unavailable', '采集失败'],
    [undefined, '未采集'],
  ])('distinguishes absent IPv6 from collection errors: %s', async (status, expected) => {
    mocked.getOverview.mockResolvedValue({ ...overview, panel: { ...overview.panel, resources: { ...overview.panel.resources, ipv6: undefined, ip_collection_status: status } } })
    const wrapper = mount(OverviewPage)
    await flushPromises()
    expect(wrapper.get('.xpanel-ip').text()).toContain('203.0.113.8')
    expect(wrapper.get('.xpanel-ip').text()).toContain(expected)
    wrapper.unmount()
  })
  it('renders overview without waiting for slow site metadata', async () => {
    mocked.loadSite.mockReturnValue(new Promise(() => {}))
    const wrapper = mount(OverviewPage)
    await flushPromises()
    expect(wrapper.find('.xpanel-gauges').exists()).toBe(true)
    expect(mocked.getOverview).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
  it('pauses hidden polling, refreshes on return, and removes its timer on unmount', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const wrapper = mount(OverviewPage)
    try {
      await flushPromises()
      visibility.mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(45000)
      expect(mocked.getOverview).toHaveBeenCalledTimes(1)
      visibility.mockReturnValue('visible')
      document.dispatchEvent(new Event('visibilitychange'))
      await flushPromises()
      expect(mocked.getOverview).toHaveBeenCalledTimes(2)
      expect(mocked.loadSite).toHaveBeenCalledTimes(1)
      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(30000)
      document.dispatchEvent(new Event('visibilitychange'))
      expect(mocked.getOverview).toHaveBeenCalledTimes(2)
    } finally { wrapper.unmount(); visibility.mockRestore(); vi.useRealTimers() }
  })
  it('opens logs and versions as dialogs and removes the old log card', async () => {
    const wrapper = mount(OverviewPage)
    await flushPromises()
    expect(wrapper.find('.xpanel-logs').exists()).toBe(false)
    await wrapper.findAll('.xpanel-controls button')[0]!.trigger('click')
    await flushPromises()
    expect(document.body.querySelector('[role="dialog"]')?.getAttribute('aria-label')).toBe('日志')
    expect(document.body.querySelector('pre')?.textContent).toContain('面板真实日志')
    document.body.querySelector<HTMLButtonElement>('[aria-label="关闭对话框"]')!.click()
    await flushPromises()
    await wrapper.findAll('.xpanel-controls button')[3]!.trigger('click')
    await flushPromises()
    expect(document.body.querySelector('[role="dialog"]')?.getAttribute('aria-label')).toBe('版本')
    expect(document.body.querySelector<HTMLInputElement>('input[type="radio"]')!.checked).toBe(true)
    expect(mocked.controlPanel).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('distinguishes disabled swap from missing metrics', async () => {
    mocked.getOverview.mockResolvedValue({ ...overview, panel: { ...overview.panel, resources: { ...overview.panel.resources, swap_used_bytes: 0, swap_total_bytes: 0 } } })
    const wrapper = mount(OverviewPage)
    await flushPromises()
    expect(wrapper.findAll('.xpanel-gauge')[2]!.text()).toContain('未启用')
    expect(wrapper.findAll('.xpanel-gauge')[2]!.text()).not.toContain('未采集')
    wrapper.unmount()
  })
  it('matches the X-Panel overview blocks and exposes every live metric', async () => {
    const wrapper = mount(OverviewPage)
    await flushPromises()

    expect(wrapper.get('.xpanel-brand h1').text()).toBe('HL-Panel')
    expect(wrapper.get('.xpanel-system').text()).toContain('系统状态')
    expect(wrapper.get('.xpanel-gauges').text()).toContain('7%')
    expect(wrapper.get('.xpanel-gauges').text()).not.toContain('24%')
    expect(wrapper.get('.xpanel-gauges').text()).toContain('交换分区')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('HL-Panel 运行状态')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('v0.1.31')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('停止')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('重启')
    expect(wrapper.get('.xpanel-runtime').text()).toContain('版本')
    expect(wrapper.get('.xpanel-controls').text()).toContain('日志')
    expect(wrapper.find('.xpanel-logs').exists()).toBe(false)
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('总数据')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('连接数')
    expect(wrapper.get('.xpanel-side').text()).toContain('系统正常运行时间')
    expect(wrapper.get('.xpanel-side').text()).toContain('系统负载')
    expect(wrapper.get('.xpanel-side').text()).toContain('整体速度')
    expect(wrapper.get('.xpanel-side').text()).toContain('IP 地址')
    expect(wrapper.get('.xpanel-side').text()).toContain('203.0.113.8')
    expect(wrapper.get('.xpanel-side').text()).toContain('0.42 / 0.37 / 0.28')
    expect(wrapper.get('.xpanel-side').text()).toContain('1.5 MB/s')
    expect(wrapper.get('.xpanel-side').text()).not.toContain('21.5 MB/s')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('7.00 GB')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('9.00 GB')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('220.00 GB')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('52')
    expect(wrapper.get('.xpanel-bottom-grid').text()).toContain('12')
    expect(wrapper.get('.xpanel-bottom-grid').text()).not.toContain('1,052')
    expect(wrapper.get('.xpanel-bottom-grid').text()).not.toContain('1,012')
    expect(wrapper.find('.overview-action-grid').exists()).toBe(false)
    wrapper.unmount()
  })

  it('sends panel control commands through the panel API', async () => {
    const wrapper = mount(OverviewPage)
    await flushPromises()
    const buttons = wrapper.findAll('.xpanel-controls button')
    const restart = buttons[2]
    if (!restart) throw new Error('restart button missing')
    await restart.trigger('click')
    await flushPromises()
    expect(mocked.controlPanel).toHaveBeenCalledWith('restart')
    wrapper.unmount()
  })
})
