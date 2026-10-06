import type { DirectPolicy, ForwardRule, GroupNetwork } from '@/api/business'

type NetworkPolicySource = Pick<GroupNetwork, 'allow_direct' | 'direct_policy'>

export function networkDirectPolicy(network: NetworkPolicySource): DirectPolicy {
  return network.direct_policy ?? (network.allow_direct ? 'OPTIONAL' : 'DISABLED')
}

export function networkAllowsDirect(network: NetworkPolicySource): boolean {
  return networkDirectPolicy(network) !== 'DISABLED'
}

export function networkAllowsExitGroup(network: NetworkPolicySource): boolean {
  return networkDirectPolicy(network) !== 'FORCED'
}

export function forwardingModeName(rule: Pick<ForwardRule, 'egress_mode'>): string {
  return rule.egress_mode === 'DIRECT' ? '入口直出' : '经出口组'
}
