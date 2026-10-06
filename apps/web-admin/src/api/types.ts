export type DeviceGroupKind = 'ENTRY' | 'EXIT' | 'EDGE' | 'HYBRID'
export type LoadBalancingStrategy = 'weighted_round_robin' | 'weighted_least_connections' | 'rendezvous_hash'
export type EndpointPoolMode = 'SINGLE_SERVICE_ENDPOINT'
export type EndpointPoolProtocol = 'vless' | 'tcp' | 'socks5'

export interface Administrator {
  id: string
  username: string
  display_name: string
  role: string
  created_at?: string
}

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  access_token: string
  expires_at: string
  user: Administrator
}

export interface OverviewResponse {
  node_count: number
  group_count: number
  online_node_count: number
  syncing_node_count: number
  failed_apply_count: number
}

export type NodeStatus = 'online' | 'offline' | 'syncing' | 'failed' | 'retired' | 'unknown'

export interface NodeDesiredGeneration {
  desired_generation: number
  applied_generation: number
  last_apply_status: string
  last_apply_message: string
}

export interface EdgeNode extends NodeDesiredGeneration {
  id: string
  name: string
  hostname: string
  platform: string
  architecture: string
  agent_version: string
  boot_id: string
  engine_versions: Record<string, string>
  resources: Record<string, unknown>
  status: NodeStatus
  capabilities: string[]
  last_heartbeat_at: string | null
  created_at: string
}

export interface NodesResponse {
  items: EdgeNode[]
}

export interface DeviceGroupMember {
  group_id: string
  node_id: string
  dial_host: string
  weight: number
  priority: number
  created_at: string
  updated_at: string
  retired_at?: string | null
}

export interface DeviceGroupMembersResponse {
  items: DeviceGroupMember[]
}

export interface DeviceGroup {
  id: string
  name: string
  kind: DeviceGroupKind
  user_group_id?: string
  hide_in_probe?: boolean
  selection_policy: LoadBalancingStrategy
  description: string
  member_count: number
  current_generation: number
  metadata_revision?: number
  created_at: string
  updated_at: string
}

export interface DeviceGroupsResponse {
  items: DeviceGroup[]
}

export interface DeleteDeviceGroupResponse {
  replayed: boolean
}

/**
 * A pool is the server-side candidate set behind one stable customer endpoint.
 * Member addresses and member URIs intentionally do not appear in this DTO.
 */
export interface EndpointPool {
  id: string
  name: string
  group_id: string
  rule_id?: string
  mode: EndpointPoolMode
  protocol: EndpointPoolProtocol
  hostname: string
  port: number
  selection_policy: LoadBalancingStrategy
  member_count: number
  healthy_candidate_count: number
  created_at: string
  updated_at: string
}

export interface EndpointPoolsResponse {
  items: EndpointPool[]
}

export interface CreateEndpointPoolRequest {
  name: string
  group_id: string
  rule_id?: string
  mode: EndpointPoolMode
  protocol: EndpointPoolProtocol
  hostname: string
  port: number
  selection_policy: LoadBalancingStrategy
  idempotency_key: string
}

export interface CreateEndpointPoolResponse {
  pool: EndpointPool
  replayed: boolean
}

export interface DeleteEndpointPoolResponse {
  replayed: boolean
}

export interface CreateDeviceGroupRequest {
  name: string
  kind: DeviceGroupKind
  user_group_id?: string
  hide_in_probe?: boolean
  selection_policy: LoadBalancingStrategy
  description: string
}

export interface UpdateDeviceGroupRequest {
  name: string
  user_group_id: string
  hide_in_probe: boolean
  selection_policy: LoadBalancingStrategy
  description: string
  revision: number
}

export interface AddDeviceGroupMemberRequest {
  node_id: string
  dial_host: string
  weight: number
  priority: number
}

export interface EnrollmentTokenRequest {
  name: string
  group_id?: string
  nezha_server_id?: number
  expires_in_seconds: number
}

export interface EnrollmentTokenResponse {
  id: string
  name: string
  token: string
  expires_at: string
  nezha_server_id?: number
}

export interface PendingEnrollmentToken {
  id: string
  name: string
  group_id: string
  expires_at: string
  created_at: string
  nezha_server_id?: number
}

export interface PendingEnrollmentTokensResponse {
  items: PendingEnrollmentToken[]
}

export interface RevokeEnrollmentTokenResponse {
  token: { id: string; revoked_at: string }
  replayed: boolean
}

export interface CreateGenerationRequest {
  engine: 'xray' | 'gost'
  config: Record<string, unknown>
  idempotency_key: string
}

export interface GroupRevision {
  id: string
  group_id: string
  generation: number
  engine: string
  config: Record<string, unknown>
  config_hash: string
  created_by: string
  created_at: string
}

export interface NodeGenerationAssignment {
  id: string
  node_id: string
  generation: number
  engine: string
  config: Record<string, unknown>
  config_hash: string
  created_at: string
}

export interface AddDeviceGroupMemberResponse {
  member: DeviceGroupMember
  assignments: NodeGenerationAssignment[]
}

export interface RetireDeviceGroupMemberResponse extends AddDeviceGroupMemberResponse {
  replayed: boolean
}

export interface UpdateDeviceGroupMemberWeightResponse extends AddDeviceGroupMemberResponse {
  replayed: boolean
}

export interface CreateGroupRevisionResponse {
  generation: GroupRevision
  assignments: NodeGenerationAssignment[]
  replayed: boolean
}
