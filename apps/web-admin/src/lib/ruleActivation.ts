import type { ForwardRule } from '@/api/business'
import { ingressStatusFor } from '@/lib/businessFormatters'

export const unsupportedActivationLabels: Record<string, string> = {
  exit_group_engine_unsupported: '经出口组暂不支持下发',
  udp_engine_unsupported: 'UDP 暂不支持下发',
  advanced_options_engine_unsupported: '高级选项暂不支持下发',
  selection_engine_unsupported: '最小连接数暂不支持下发',
}

export function ruleDisplayStatus(rule: ForwardRule): string {
  if (rule.paused) return rule.status
  if (rule.activation_reason && unsupportedActivationLabels[rule.activation_reason]) return rule.activation_reason
  if (ingressStatusFor(rule) === 'pending_reality_parameters') return 'pending_reality_parameters'
  if (rule.activation_reason === 'vless_runtime_material_pending') return rule.activation_reason
  return rule.status
}

export function ruleActivationNotice(rule: ForwardRule): string {
  if (rule.paused) return '已暂停'
  const status = ruleDisplayStatus(rule)
  return unsupportedActivationLabels[status] ?? (status === 'pending_reality_parameters' ? '待补 Reality 参数' : status === 'vless_runtime_material_pending' ? '等待节点运行材料' : '待激活')
}
