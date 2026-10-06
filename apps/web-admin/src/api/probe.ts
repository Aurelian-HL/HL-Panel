import { HttpClient } from './http'

export type ProbeUpstreamStatus = 'disabled' | 'ok' | 'unavailable'

export interface ProbeMember {
  node_id: string
  link_status: 'linked' | 'unlinked' | 'unmanaged'
  nezha_server_id?: number
  online?: boolean
  name?: string
  host_platform?: string
  host_platform_version?: string
  host_architecture?: string
  host_boot_time?: number
  agent_version?: string
  registered_at?: string
  ipv4?: string
  ipv6?: string
  country_code?: string
  uptime_seconds?: number
  cpu_percent?: number
  memory_used_bytes?: number
  memory_total_bytes?: number
  disk_used_bytes?: number
  disk_total_bytes?: number
  net_in_speed_bytes_per_second?: number
  net_out_speed_bytes_per_second?: number
  net_in_transfer_bytes?: number
  net_out_transfer_bytes?: number
  tcp_conn_count?: number
  udp_conn_count?: number
  sampled_at?: string
}

export interface ProbeGroupResponse {
  items: ProbeMember[]
  upstream_status: ProbeUpstreamStatus
}

function object(value: unknown, name: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${name} 必须是对象`)
  return value as Record<string, unknown>
}

function optionalText(value: unknown, name: string): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') throw new Error(`${name} 必须是字符串`)
  return value
}

function optionalNumber(value: unknown, name: string): number | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) throw new Error(`${name} 必须是非负数字`)
  return value
}

function optionalCount(value: unknown, name: string): number | undefined {
  const count = optionalNumber(value, name)
  if (count !== undefined && !Number.isSafeInteger(count)) throw new Error(`${name} 必须是整数`)
  return count
}

export function parseProbeGroupResponse(value: unknown): ProbeGroupResponse {
  const body = object(value, '探针响应')
  if (!['disabled', 'ok', 'unavailable'].includes(body.upstream_status as string)) throw new Error('探针上游状态无效')
  if (!Array.isArray(body.items)) throw new Error('探针成员必须是数组')
  const items = body.items.map((raw, index): ProbeMember => {
    const item = object(raw, `探针成员 ${index}`)
    if (typeof item.node_id !== 'string' || !item.node_id) throw new Error(`探针成员 ${index} 缺少节点 ID`)
    if (item.link_status !== 'linked' && item.link_status !== 'unlinked' && item.link_status !== 'unmanaged') throw new Error(`探针成员 ${index} 关联状态无效`)
    if (item.online !== undefined && typeof item.online !== 'boolean') throw new Error(`探针成员 ${index} 在线状态无效`)
    const nezhaServerID = optionalNumber(item.nezha_server_id, '哪吒服务器 ID')
    if (nezhaServerID !== undefined && (!Number.isSafeInteger(nezhaServerID) || nezhaServerID < 1)) throw new Error('哪吒服务器 ID 无效')
    return {
      node_id: item.node_id,
      link_status: item.link_status,
      nezha_server_id: nezhaServerID,
      online: item.online as boolean | undefined,
      name: optionalText(item.name, '节点名称'),
      host_platform: optionalText(item.host_platform, '主机系统'),
      host_platform_version: optionalText(item.host_platform_version, '系统版本'),
      host_architecture: optionalText(item.host_architecture, '主机架构'),
      host_boot_time: optionalNumber(item.host_boot_time, '开机时间'),
      agent_version: optionalText(item.agent_version, '探针版本'),
      registered_at: optionalText(item.registered_at, '探针注册时间'),
      ipv4: optionalText(item.ipv4, 'IPv4'),
      ipv6: optionalText(item.ipv6, 'IPv6'),
      country_code: optionalText(item.country_code, '国家代码'),
      uptime_seconds: optionalNumber(item.uptime_seconds, '开机时长'),
      cpu_percent: optionalNumber(item.cpu_percent, 'CPU 使用率'),
      memory_used_bytes: optionalNumber(item.memory_used_bytes, '已用内存'),
      memory_total_bytes: optionalNumber(item.memory_total_bytes, '总内存'),
      disk_used_bytes: optionalNumber(item.disk_used_bytes, '已用磁盘'),
      disk_total_bytes: optionalNumber(item.disk_total_bytes, '总磁盘'),
      net_in_speed_bytes_per_second: optionalNumber(item.net_in_speed_bytes_per_second, '下行速率'),
      net_out_speed_bytes_per_second: optionalNumber(item.net_out_speed_bytes_per_second, '上行速率'),
      net_in_transfer_bytes: optionalNumber(item.net_in_transfer_bytes, '累计下行'),
      net_out_transfer_bytes: optionalNumber(item.net_out_transfer_bytes, '累计上行'),
      tcp_conn_count: optionalCount(item.tcp_conn_count, 'TCP 连接数'),
      udp_conn_count: optionalCount(item.udp_conn_count, 'UDP 连接数'),
      sampled_at: optionalText(item.sampled_at, '采样时间'),
    }
  })
  return { items, upstream_status: body.upstream_status as ProbeUpstreamStatus }
}

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

export const probeApi = {
  async getInventory(): Promise<ProbeGroupResponse> {
    return parseProbeGroupResponse(await http.request('/monitoring/nezha/servers'))
  },
  async getGroup(groupId: string): Promise<ProbeGroupResponse> {
    return parseProbeGroupResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/monitoring/nezha`))
  },
}
