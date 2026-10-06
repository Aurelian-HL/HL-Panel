export type UsageScope = 'site' | 'customer' | 'rule' | 'device_group'

export interface UsageQuery {
  scope: UsageScope
  scope_id?: string
  from?: string
  to?: string
  page?: number
  page_size?: number
}

export type EnforcementReason = 'customer_disabled' | 'customer_expired' | 'quota_exhausted'
export type EnforcementAction = 'disable_customer_access' | 'enable_customer_access'
export type EnforcementStatus = 'pending' | 'applied' | 'apply_failed' | 'revoke_pending' | 'revoked' | 'revoke_failed'

export interface EnforcementDecision {
  id: string
  customer_id: string
  rule_id: string
  protocol: 'tcp' | 'udp'
  reason: EnforcementReason
  action: EnforcementAction
  status: EnforcementStatus
  trigger_node_id: string
  trigger_boot_id: string
  trigger_sequence: number
  customer_charged_bytes: number
  traffic_limit_bytes: number
  created_at: string
  updated_at: string
  revision: number
  last_error: string
}

export interface UsageRecord {
  node_id: string
  boot_id: string
  sequence: number
  customer_id: string
  rule_id: string
  entry_group_id: string
  exit_group_id: string
  occurred_at: string
  period_started_at: string
  period_ended_at: string
  rule_actual_bytes: number
  customer_actual_bytes: number
  entry_multiplier_micros: number
  exit_multiplier_micros: number
  charged_bytes: number
  payload_sha256: string
  received_at: string
  enforcement_decision?: EnforcementDecision
}

export interface UsageTotals {
  rule_actual_bytes: number
  customer_actual_bytes: number
  charged_bytes: number
}

export interface UsageResponse {
  items: UsageRecord[]
  totals: UsageTotals
  total: number
  page: number
  page_size: number
}

export interface EnforcementRevokeResponse {
  decision: EnforcementDecision
  replayed: boolean
}
