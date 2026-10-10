export interface UserGroupInput {
  name: string
  description: string
  allowed_entry_group_ids: string[]
  allowed_exit_group_ids: string[]
  allow_direct: boolean
  revision: number
}
export interface UserGroup extends UserGroupInput { id: string }

export interface CustomerInput {
  username: string
  display_name: string
  user_group_id: string
  password?: string
  disabled: boolean
  expires_at: string | null
  traffic_limit_bytes: number
  max_rules: number
  speed_limit_mbps: number
  ip_limit: number
  connection_limit: number
  revision: number
}
export interface Customer extends Omit<CustomerInput, 'password'> {
  id: string
  traffic_used_bytes: number
  effective_status: 'active' | 'disabled' | 'expired' | 'quota_exhausted'
  created_at: string
  updated_at: string
}

export type DirectPolicy = 'DISABLED' | 'OPTIONAL' | 'FORCED'

export interface PortRange {
  start: number
  end: number
}

export interface GroupNetworkInput {
  connect_host: string
  port_start: number
  port_end: number
  port_ranges?: PortRange[]
  direct_policy: DirectPolicy
  allow_direct: boolean
  allowed_user_group_ids?: string[]
  allowed_entry_group_ids?: string[]
  allowed_exit_group_ids: string[]
  fallback_exit_group_id?: string
  traffic_multiplier: number
  revision: number
}
export interface GroupNetwork extends Omit<GroupNetworkInput, 'direct_policy'> {
  group_id: string
  // Older servers only return allow_direct. Keep reads compatible during rollout.
  direct_policy?: DirectPolicy
}

export interface ForwardTarget { host: string; port: number }
/** Customer-facing listener protocol. The optional shape keeps older NY
 * records readable while new writes always send an explicit value. */
export type IngressProtocol = 'tcp' | 'udp' | 'socks5' | 'vless_reality'
export type IngressStatus = 'ready' | 'pending_reality_parameters'
export type ActivationReason = 'vless_runtime_material_pending' | 'socks5_runtime_material_pending' | string
export interface ForwardRuleInput {
  name: string
  customer_id?: string
  rule_group_id: string
  entry_group_id: string
  exit_group_id: string
  egress_mode: 'DIRECT' | 'EXIT_GROUP'
  ingress_protocol?: IngressProtocol
  vless_flow?: 'xtls-rprx-vision' | string
  reality_server_name?: string
  reality_public_key?: string
  reality_short_id?: string
  reality_destination?: string
  /** VLESS decrypted traffic's node-side outbound. Password is write-only. */
  vless_outbound_mode?: 'DIRECT' | 'SOCKS5'
  vless_socks5_host?: string
  vless_socks5_port?: number
  vless_socks5_username?: string
  vless_socks5_password?: string
  protocol: 'tcp' | 'udp'
  listen_port: number
  targets: ForwardTarget[]
  selection_policy: 'round_robin' | 'random' | 'ip_hash' | 'least_load' | 'failover'
  accept_proxy_protocol?: boolean
  send_proxy_protocol?: 0 | 1 | 2 | 3
  speed_limit_mbps?: number
  ip_limit?: number
  connection_limit?: number
  traffic_limit_bytes?: number
  traffic_quota_monthly?: boolean
  paused: boolean
  description: string
  revision: number
}
export interface ForwardRule extends Omit<ForwardRuleInput, 'vless_socks5_password'> {
  id: string
  customer_id: string
  status: string
  traffic_used_bytes?: number
  ingress_status?: IngressStatus
  deployed?: boolean
  /** Non-secret reason why the current isolated slice has not activated a rule. */
  activation_reason?: ActivationReason
}

export interface ForwardingTargetProbe {
  host: string
  port: number
  address: string
  reachable: boolean
  latency_ms: number
  error_code?: string
  message?: string
}

export interface ForwardingConnection {
  uri: string
  name: string
  endpoint: string
  status: string
}

export interface VlessIdentity {
  id: string
  customer_id: string
  forwarding_rule_id: string
  endpoint_pool_id: string
  state: 'active' | 'revoked' | string
  revision: number
  created_at: string
  updated_at: string
  revoked_at?: string | null
}

export interface VlessIdentityMutation {
  identity: VlessIdentity
  replayed: boolean
}

export interface RuleGroupInput {
  name: string
  description: string
  revision: number
}
export interface RuleGroup extends RuleGroupInput {
  id: string
  created_at: string
  updated_at: string
}

export type RuleBatchOperation = 'pause' | 'resume' | 'move_group' | 'delete'
export interface RuleBatchInput {
  operation: RuleBatchOperation
  rule_ids: string[]
  rule_group_id: string
  expected_revisions: Record<string, number>
}
export interface RuleBatchResult {
  operation: RuleBatchOperation
  rule_group_id: string
  items: ForwardRule[]
}

export type ForwardImportFormat = 'auto' | 'ny_text' | 'json_v1'
export type ForwardImportOperation = 'create' | 'update' | 'upsert'
export type ForwardImportAction = 'create' | 'update'

export interface ForwardImportMapping {
  customer_id: string
  rule_group_id: string
  entry_group_id: string
  egress_mode: '' | 'DIRECT' | 'EXIT_GROUP'
  exit_group_id: string
  ingress_protocol?: '' | IngressProtocol
  protocol: '' | 'tcp' | 'udp'
  selection_policy: '' | ForwardRuleInput['selection_policy']
}

export interface ForwardTransferRequest {
  format: ForwardImportFormat
  content: string
  mapping: ForwardImportMapping
}

export interface ForwardImportIssue { code: string; message: string }
export interface ForwardImportRow {
  line: number
  operation: ForwardImportOperation
  action?: ForwardImportAction
  source_id?: string
  request: ForwardRuleInput
  resolved_listen_port?: number
  issues: ForwardImportIssue[] | null
}
export interface ForwardImportPreview {
  format: Exclude<ForwardImportFormat, 'auto'>
  total: number
  valid: number
  invalid: number
  rows: ForwardImportRow[]
  global_issues: ForwardImportIssue[] | null
}
export interface ForwardImportResult { created: number; updated: number; items: ForwardRule[] }

export interface ForwardExportRule {
  operation: ForwardImportOperation
  id: string
  rule: ForwardRuleInput
}
export interface ForwardExportDocument {
  schema_version: 'nyvp.forwarding-rules/v1'
  exported_at: string
  rules: ForwardExportRule[]
}
