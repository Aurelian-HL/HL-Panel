import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ getOverview: vi.fn(), loadSite: vi.fn() }))

vi.mock('@/api', () => ({ api: { getOverview: mocked.getOverview } }))
vi.mock('@/stores/site', () => ({
  siteStore: {
    info: { value: { platform_version: 'v0.1.30', build_time: '2026-10-07T10:00:00Z', announcements: [] } },
    siteName: { value: 'HL-panel' },
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
      resources: { cpu_percent: 24, memory_used_bytes: 2 * 1024 ** 3, memory_total_bytes: 8 * 1024 ** 3, disk_used_bytes: 40 * 1024 ** 3, disk_total_bytes: 100 * 1024 ** 3, net_in_speed_bytes_per_second: 1024 ** 2, net_out_speed_bytes_per_second: 512 * 1024, ipv4: '198.51.100.10', uptime_seconds: 90061, tcp_conn_count: 12, udp_conn_count: 3 },
      status: 'online', capabilities: ['xray'], desired_generation: 2, applied_generation: 2, last_apply_status: 'succeeded', last_apply_message: '', last_heartbeat_at: '2026-10-07T10:00:00Z', created_at: '2026-10-01T00:00:00Z',
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
})

describe('OverviewPage', () => {
  it('renders live summary, node resources and complete shortcut navigation', async () => {
    const wrapper = mount(OverviewPage, { global: { stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } } } })
    await flushPromises()

    expect(wrapper.get('.overview-health').text()).toContain('节点正在同步')
    expect(wrapper.get('.overview-hero__facts').text()).toContain('50%')
    expect(wrapper.get('.overview-metrics').text()).toContain('节点总数2')
    expect(wrapper.get('.overview-summary-card').text()).toContain('v0.1.30')
    expect(wrapper.get('.overview-resource-list').text()).toContain('24%')
    expect(wrapper.get('.system-node-grid').text()).toContain('香港出口')
    expect(wrapper.findAll('.overview-action')).toHaveLength(6)
    expect(wrapper.get('a[href="/device-groups"]').text()).toContain('设备组管理')
    wrapper.unmount()
  })
})
