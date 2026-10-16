import type { ForwardRule } from '@/api/business'

export function ruleConnectionPresentation(rule: ForwardRule, elapsed: number) {
  const sample = rule.connection
  const inactive = rule.paused || !['active', 'pending_activation'].includes(rule.status)
  if (!sample || inactive || elapsed >= 3000) return { status: 'offline', label: '不在线', title: inactive ? '规则已暂停或不可用' : '暂无新鲜连接采样' }
  if (sample.status === 'online' && sample.connections > 0) {
    const warning = sample.error_nodes ? `；另有 ${sample.error_nodes} 台节点异常` : ''
    return { status: 'online', label: '在线', title: `当前 TCP 连接 ${sample.connections} 条${warning}。连接数不是人数，不代表出口可达。` }
  }
  if (sample.status === 'error') return { status: 'error', label: '错误', title: '节点配置应用失败或规则监听端口缺失' }
  return { status: 'offline', label: '不在线', title: sample.reason === 'no_connections' ? '监听正常，当前没有 TCP 连接' : sample.reason === 'tcp_only' ? '此状态仅统计 TCP 连接' : '暂无新鲜连接采样' }
}
