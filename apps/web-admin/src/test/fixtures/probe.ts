import type { DeviceGroup } from '@/api'
import type { ProbeGroupResponse } from '@/api/probe'

export const probeGroups: DeviceGroup[] = [
  {
    id: 'group-gz', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin',
    description: '', member_count: 2, current_generation: 1,
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
  },
  {
    id: 'group-hk', name: '香港出口', kind: 'EXIT', selection_policy: 'weighted_round_robin',
    description: '', member_count: 1, current_generation: 1,
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
  },
]

export const completeProbeSample: ProbeGroupResponse = {
  upstream_status: 'ok',
  items: [
    {
      node_id: 'node-gz-1', link_status: 'linked', nezha_server_id: 17, online: true,
      name: '广州电信入口', ipv4: '121.14.61.68', ipv6: '2001:db8::1', country_code: 'CN',
      host_platform: 'debian', host_platform_version: '13.2', host_architecture: 'x86_64',
      host_boot_time: 1750000000, agent_version: 'v2.3.5', registered_at: '2026-10-01T00:00:00Z',
      uptime_seconds: 90061, cpu_percent: 36.5,
      memory_used_bytes: 2147483648, memory_total_bytes: 4294967296,
      disk_used_bytes: 10737418240, disk_total_bytes: 42949672960,
      net_in_speed_bytes_per_second: 1048576, net_out_speed_bytes_per_second: 524288,
      net_in_transfer_bytes: 1073741824, net_out_transfer_bytes: 536870912,
      tcp_conn_count: 2104, udp_conn_count: 9,
      sampled_at: new Date().toISOString(),
    },
    {
      node_id: 'node-gz-2', link_status: 'linked', nezha_server_id: 18, online: false,
      name: '广州联通入口', ipv4: '122.13.18.216', country_code: 'CN',
      cpu_percent: 98, net_in_speed_bytes_per_second: 2097152,
      sampled_at: '2026-10-03T23:00:00Z',
    },
  ],
}

export const unavailableProbeSample: ProbeGroupResponse = {
  upstream_status: 'unavailable',
  items: [
    { node_id: 'node-hk-1', link_status: 'unlinked', name: '香港出口节点' },
    { node_id: 'node-hk-2', link_status: 'linked', nezha_server_id: 19, online: true, name: '香港备用节点', cpu_percent: 90, net_out_speed_bytes_per_second: 1048576, sampled_at: '2026-10-03T00:00:00Z' },
  ],
}
