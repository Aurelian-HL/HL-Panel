import type { ForwardRule } from '@/api/business'

export interface RuleConnectionPresentation {
  status: 'online' | 'offline' | 'error'
  label: string
  title: string
  details: string[]
}

export function ruleConnectionPresentation(rule: ForwardRule, elapsed: number): RuleConnectionPresentation {
  const sample = rule.connection
  const details = ['连接采样只统计入口监听端口的 TCP 连接；连接数不是人数，也不代表出口可达。右侧状态单独表示配置及协议验证结果。']
  const present = (status: RuleConnectionPresentation['status'], label: string, title: string): RuleConnectionPresentation => ({ status, label, title, details: [title, ...details] })
  if (rule.paused) return present('offline', '已暂停', '规则已暂停，不显示活动连接状态。')
  if (!['active', 'pending_activation'].includes(rule.status)) return present('offline', '未启用', '规则当前未启用，不显示活动连接状态。')
  if (!sample) return present('offline', '待采样', '尚未收到这条规则的连接采样，不能据此判断线路断开。')
  if (sample.sampled_at) {
    const at = new Date(sample.sampled_at)
    if (Number.isFinite(at.getTime())) details.unshift('节点采样时间：' + at.toLocaleString('zh-CN', { hour12: false }))
  }
  if (sample.sampled_nodes) details.push('已采样节点：' + sample.sampled_nodes + ' 台')
  if (sample.missing_listener_nodes) details.push('未检测到监听端口：' + sample.missing_listener_nodes + ' 台节点')
  if (sample.apply_failed_nodes) details.push('节点最新配置更新失败：' + sample.apply_failed_nodes + ' 台；这不等于已有规则监听已失效，可在设备探针查看应用失败详情。')
  if (sample.pending_nodes) details.push('尚未确认当前规则配置：' + sample.pending_nodes + ' 台节点')
  if (sample.stale_nodes) details.push('心跳采样缺失或过期：' + sample.stale_nodes + ' 台节点')
  // The server supplies the remaining lease, so re-fetching the same heartbeat
  // cannot reset its age. The short fallback keeps older panels compatible.
  const ttl = typeof sample.valid_for_ms === 'number' && Number.isFinite(sample.valid_for_ms)
    ? Math.max(0, sample.valid_for_ms) : 3000
  if (sample.reason === 'sample_expired' || elapsed >= ttl && (sample.sampled_at || sample.status !== 'offline' || sample.reason === 'no_connections')) {
    return present('offline', '采样过期', '连接采样已过期，当前连接情况未知；不会据此判定线路不可用。')
  }
  if (sample.status === 'online' && sample.connections > 0) {
    if (sample.error_nodes) return present('error', '有连接 · 部分异常', '当前 TCP 连接 ' + sample.connections + ' 条；另有 ' + sample.error_nodes + ' 台节点异常，请展开查看。')
    return present('online', '有连接', '当前 TCP 连接 ' + sample.connections + ' 条。')
  }
  if (sample.reason === 'listener_missing') return present('error', '监听异常', '节点最新采样未检测到此规则的 TCP 监听端口；不等于所有节点或客户端路径都失败。')
  if (sample.reason === 'node_apply_failed') return present('error', '节点更新异常', '节点最新配置更新失败，尚不能据此判断这条已有规则是否仍可用。')
  if (sample.status === 'error') return present('error', '采样异常', '节点报告配置应用或监听异常；旧版本未区分具体原因，请查看设备探针的应用状态并升级节点。')
  if (sample.reason === 'no_connections') return present('offline', '空闲', '已检测到监听端口，当前 TCP 连接为 0；空闲不等于线路不通。')
  if (sample.reason === 'tcp_only') return present('offline', '不适用', '此处仅统计 TCP 连接，不用于判断 UDP 规则是否可达。')
  if (sample.reason === 'deployment_pending') return present('offline', '待配置确认', '当前配置回执尚未确认，暂不将端口连接数归属到这条规则。')
  return present('offline', '待采样', '尚未收到这条规则的有效连接采样，不能据此判断线路断开。')
}
