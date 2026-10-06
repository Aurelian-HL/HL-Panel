export type CustomerStatus = 'active' | 'disabled' | 'expired' | 'quota_exhausted'

export interface CustomerProfile {
  id: string
  username: string
  display_name: string
  user_type: string
  user_group_name: string
  expires_at: string | null
  traffic_used_bytes: number
  traffic_limit_bytes: number
  max_rules: number
  speed_limit_mbps: number
  ip_limit: number
  connection_limit: number
  effective_status: CustomerStatus
}

export interface LoginResult {
  access_token: string
  expires_at: string
  user: CustomerProfile
}

export interface PortalInfoItem { label: string; value: string }
export interface PortalInfo {
  site_name: string
  panel_version: string
  announcement: { title: string; content: string; updated_at: string | null }
  site_info: PortalInfoItem[]
  backend_info: PortalInfoItem[]
}

export interface RuleTarget { host: string; port: number }
export interface CustomerRule {
  id: string
  name: string
  /** Customer-facing handshake; distinct from the underlying TCP/UDP transport. */
  ingress_protocol: 'tcp' | 'udp' | 'socks5' | 'vless_reality'
  protocol: string
  connect_host: string
  listen_port: number
  route_description: string
  targets: RuleTarget[]
  paused: boolean
  status: string
  deployed: boolean
  revision: number
}

export interface RuleGroupOption { id: string; name: string }
export interface EntryGroupOption extends RuleGroupOption {
  connect_host: string
  port_ranges: { start: number; end: number }[]
  allow_direct: boolean
  allowed_exit_group_ids: string[]
}
export interface CustomerRuleOptions {
  entry_groups: EntryGroupOption[]
  exit_groups: RuleGroupOption[]
  rule_groups: RuleGroupOption[]
}
export interface CustomerRuleInput {
  name: string
  rule_group_id: string
  entry_group_id: string
  exit_group_id: string
  egress_mode: 'DIRECT' | 'EXIT_GROUP'
  /** Customer-facing listener; SOCKS5 remains distinct from raw NY TCP. */
  ingress_protocol: 'tcp' | 'udp' | 'socks5' | 'vless_reality'
  protocol: 'tcp' | 'udp'
  vless_flow: string
  reality_server_name: string
  reality_public_key: string
  reality_short_id: string
  reality_destination: string
  listen_port: number
  targets: RuleTarget[]
  selection_policy: 'round_robin' | 'random' | 'ip_hash' | 'least_load' | 'failover'
  accept_proxy_protocol: boolean
  send_proxy_protocol: number
  speed_limit_mbps: number
  ip_limit: number
  connection_limit: number
  paused: boolean
  description: string
  revision: number
}
export interface CustomerRuleDetail extends CustomerRuleInput { id: string; customer_id: string }
export interface CustomerRuleBatchResult { items: CustomerRuleDetail[] }

export interface UsageInfo {
  traffic_used_bytes: number
  traffic_limit_bytes: number
  remaining_bytes: number | null
  updated_at: string
}
