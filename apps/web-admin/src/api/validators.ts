import type {
  CreateEndpointPoolResponse,
  DeleteEndpointPoolResponse,
  DeviceGroup,
  DeviceGroupsResponse,
  DeleteDeviceGroupResponse,
  DeviceGroupMember,
  DeviceGroupMembersResponse,
  LoadBalancingStrategy,
  EdgeNode,
  EndpointPool,
  EndpointPoolMode,
  EndpointPoolProtocol,
  EndpointPoolsResponse,
  AddDeviceGroupMemberResponse,
  RetireDeviceGroupMemberResponse,
  UpdateDeviceGroupMemberWeightResponse,
  CreateGroupRevisionResponse,
  LoginResponse,
  NodesResponse,
  OverviewResponse,
} from './types'

export class ContractError extends Error {
  constructor(message: string) {
    super(`API 响应不符合契约：${message}`)
    this.name = 'ContractError'
  }
}

function record(value: unknown, name: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new ContractError(`${name} 必须是对象`)
  return value as Record<string, unknown>
}

function text(value: unknown, name: string): string {
  if (typeof value !== 'string') throw new ContractError(`${name} 必须是字符串`)
  return value
}

function numberValue(value: unknown, name: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) throw new ContractError(`${name} 必须是数字`)
  return value
}

function booleanValue(value: unknown, name: string): boolean {
  if (typeof value !== 'boolean') throw new ContractError(`${name} 必须是布尔值`)
  return value
}

function optionalText(value: unknown, name: string): string {
  return value === undefined || value === null ? '' : text(value, name)
}

function nullableText(value: unknown, name: string): string | null {
  return value === undefined || value === null || value === '' ? null : text(value, name)
}

function strings(value: unknown, name: string): string[] {
  if (value === undefined || value === null) return []
  if (!Array.isArray(value) || value.some((item) => typeof item !== 'string')) throw new ContractError(`${name} 必须是字符串数组`)
  return value as string[]
}

function stringRecord(value: unknown, name: string): Record<string, string> {
  if (value === undefined || value === null) return {}
  const body = record(value, name)
  if (Object.values(body).some((item) => typeof item !== 'string')) throw new ContractError(`${name} 的值必须是字符串`)
  return body as Record<string, string>
}

function unknownRecord(value: unknown, name: string): Record<string, unknown> {
  return value === undefined || value === null ? {} : record(value, name)
}

function selectionPolicy(value: unknown, name: string): LoadBalancingStrategy {
  const raw = value === undefined || value === null || value === '' ? 'weighted_least_connections' : text(value, name)
  if (!['weighted_round_robin', 'weighted_least_connections', 'rendezvous_hash'].includes(raw)) throw new ContractError(`未知服务端调度策略 ${raw}`)
  return raw as LoadBalancingStrategy
}

function endpointMode(value: unknown, name: string): EndpointPoolMode {
  const raw = text(value, name)
  if (raw !== 'SINGLE_SERVICE_ENDPOINT') throw new ContractError(`未知端点模式 ${raw}`)
  return raw
}

function endpointProtocol(value: unknown, name: string): EndpointPoolProtocol {
  const raw = text(value, name)
  if (raw !== 'vless' && raw !== 'tcp' && raw !== 'socks5') throw new ContractError(`未知端点协议 ${raw}`)
  return raw
}

function nonNegativeInteger(value: unknown, name: string): number {
  const parsed = numberValue(value, name)
  if (!Number.isInteger(parsed) || parsed < 0) throw new ContractError(`${name} 必须是非负整数`)
  return parsed
}

function endpointPool(value: unknown, index = 0): EndpointPool {
  const item = record(value, `endpoint_pools.items[${index}]`)
  const port = numberValue(item.port, `endpoint_pools.items[${index}].port`)
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new ContractError(`endpoint_pools.items[${index}].port 必须是 1 至 65535 的整数`)
  return {
    id: text(item.id, `endpoint_pools.items[${index}].id`),
    name: text(item.name, `endpoint_pools.items[${index}].name`),
    group_id: text(item.group_id, `endpoint_pools.items[${index}].group_id`),
    mode: endpointMode(item.mode, `endpoint_pools.items[${index}].mode`),
    protocol: endpointProtocol(item.protocol, `endpoint_pools.items[${index}].protocol`),
    hostname: text(item.hostname, `endpoint_pools.items[${index}].hostname`),
    port,
    selection_policy: selectionPolicy(item.selection_policy, `endpoint_pools.items[${index}].selection_policy`),
    member_count: nonNegativeInteger(item.member_count, `endpoint_pools.items[${index}].member_count`),
    healthy_candidate_count: nonNegativeInteger(item.healthy_candidate_count, `endpoint_pools.items[${index}].healthy_candidate_count`),
    created_at: text(item.created_at, `endpoint_pools.items[${index}].created_at`),
    updated_at: text(item.updated_at, `endpoint_pools.items[${index}].updated_at`),
  }
}

export function parseEndpointPoolsResponse(value: unknown): EndpointPoolsResponse {
  const body = record(value, 'endpoint_pools')
  if (!Array.isArray(body.items)) throw new ContractError('endpoint_pools.items 必须是数组')
  return { items: body.items.map(endpointPool) }
}

export function parseEndpointPoolResponse(value: unknown): CreateEndpointPoolResponse {
  const body = record(value, 'create_endpoint_pool')
  if (typeof body.replayed !== 'boolean') throw new ContractError('endpoint_pool.replayed 必须是布尔值')
  return { pool: endpointPool(body.pool, 0), replayed: body.replayed }
}

export function parseDeleteEndpointPoolResponse(value: unknown): DeleteEndpointPoolResponse {
  const body = record(value, 'delete_endpoint_pool')
  if (typeof body.replayed !== 'boolean') throw new ContractError('delete_endpoint_pool.replayed 必须是布尔值')
  return { replayed: body.replayed }
}

export function parseLoginResponse(value: unknown): LoginResponse {
  const body = record(value, 'login')
  const user = record(body.user, 'user')
  return {
    access_token: text(body.access_token, 'access_token'),
    expires_at: text(body.expires_at, 'expires_at'),
    user: {
      id: text(user.id, 'user.id'),
      username: text(user.username, 'user.username'),
      display_name: optionalText(user.display_name, 'user.display_name') || text(user.username, 'user.username'),
      role: optionalText(user.role, 'user.role') || 'administrator',
      created_at: optionalText(user.created_at, 'user.created_at'),
      must_change_password: user.must_change_password === undefined ? false : booleanValue(user.must_change_password, 'user.must_change_password'),
    },
  }
}

export function parseOverviewResponse(value: unknown): OverviewResponse {
  const body = record(value, 'overview')
  return {
    node_count: numberValue(body.node_count, 'node_count'),
    group_count: numberValue(body.group_count, 'group_count'),
    online_node_count: numberValue(body.online_node_count, 'online_node_count'),
    syncing_node_count: numberValue(body.syncing_node_count, 'syncing_node_count'),
    failed_apply_count: numberValue(body.failed_apply_count, 'failed_apply_count'),
  }
}

function parseNode(value: unknown, index: number): EdgeNode {
  const item = record(value, `nodes.items[${index}]`)
  const rawStatus = optionalText(item.status, `nodes.items[${index}].status`).toLowerCase()
  const status = ['online', 'offline', 'syncing', 'failed', 'retired'].includes(rawStatus) ? rawStatus : 'unknown'
  return {
    id: text(item.id, `nodes.items[${index}].id`),
    name: optionalText(item.name, `nodes.items[${index}].name`),
    hostname: text(item.hostname, `nodes.items[${index}].hostname`),
    platform: optionalText(item.platform, `nodes.items[${index}].platform`),
    architecture: optionalText(item.architecture, `nodes.items[${index}].architecture`),
    agent_version: optionalText(item.agent_version, `nodes.items[${index}].agent_version`),
    boot_id: optionalText(item.boot_id, `nodes.items[${index}].boot_id`),
    engine_versions: stringRecord(item.engine_versions, `nodes.items[${index}].engine_versions`),
    resources: unknownRecord(item.resources, `nodes.items[${index}].resources`),
    status: status as EdgeNode['status'],
    capabilities: strings(item.capabilities, `nodes.items[${index}].capabilities`),
    desired_generation: numberValue(item.desired_generation ?? 0, `nodes.items[${index}].desired_generation`),
    applied_generation: numberValue(item.applied_generation ?? 0, `nodes.items[${index}].applied_generation`),
    last_apply_status: optionalText(item.last_apply_status, `nodes.items[${index}].last_apply_status`),
    last_apply_message: optionalText(item.last_apply_message, `nodes.items[${index}].last_apply_message`),
    last_heartbeat_at: nullableText(item.last_heartbeat_at, `nodes.items[${index}].last_heartbeat_at`),
    created_at: optionalText(item.created_at, `nodes.items[${index}].created_at`),
  }
}

export function parseNodesResponse(value: unknown): NodesResponse {
  const body = record(value, 'nodes')
  if (!Array.isArray(body.items)) throw new ContractError('nodes.items 必须是数组')
  return { items: body.items.map(parseNode) }
}

function parseGroup(value: unknown, index = 0): DeviceGroup {
  const item = record(value, `device_groups.items[${index}]`)
  const rawKind = text(item.kind, `device_groups.items[${index}].kind`)
  if (!['ENTRY', 'EXIT', 'EDGE', 'HYBRID'].includes(rawKind)) throw new ContractError(`未知设备组类型 ${rawKind}`)
  return {
    id: text(item.id, `device_groups.items[${index}].id`),
    name: text(item.name, `device_groups.items[${index}].name`),
    kind: rawKind as DeviceGroup['kind'],
    user_group_id: optionalText(item.user_group_id, `device_groups.items[${index}].user_group_id`),
    hide_in_probe: item.hide_in_probe === true,
    selection_policy: selectionPolicy(item.selection_policy, `device_groups.items[${index}].selection_policy`),
    description: optionalText(item.description, `device_groups.items[${index}].description`),
    member_count: numberValue(item.member_count ?? 0, `device_groups.items[${index}].member_count`),
    current_generation: numberValue(item.current_generation ?? 0, `device_groups.items[${index}].current_generation`),
    metadata_revision: numberValue(item.metadata_revision ?? 0, `device_groups.items[${index}].metadata_revision`),
    created_at: optionalText(item.created_at, `device_groups.items[${index}].created_at`),
    updated_at: optionalText(item.updated_at, `device_groups.items[${index}].updated_at`),
  }
}

export function parseDeviceGroupsResponse(value: unknown): DeviceGroupsResponse {
  const body = record(value, 'device_groups')
  if (!Array.isArray(body.items)) throw new ContractError('device_groups.items 必须是数组')
  return { items: body.items.map(parseGroup) }
}

export function parseDeviceGroup(value: unknown): DeviceGroup {
  const body = record(value, 'create_device_group')
  return parseGroup(body.group)
}

export function parseDeleteDeviceGroupResponse(value: unknown): DeleteDeviceGroupResponse {
  const body = record(value, 'delete_device_group')
  if (typeof body.replayed !== 'boolean') throw new ContractError('delete_device_group.replayed 必须是布尔值')
  return { replayed: body.replayed }
}

function parseAssignment(value: unknown, index: number) {
  const body = record(value, `assignments[${index}]`)
  return {
    id: text(body.id, `assignments[${index}].id`),
    node_id: text(body.node_id, `assignments[${index}].node_id`),
    generation: numberValue(body.generation, `assignments[${index}].generation`),
    engine: text(body.engine, `assignments[${index}].engine`),
    config: unknownRecord(body.config, `assignments[${index}].config`),
    config_hash: text(body.config_hash, `assignments[${index}].config_hash`),
    created_at: optionalText(body.created_at, `assignments[${index}].created_at`),
  }
}

function parseAssignments(value: unknown) {
  if (!Array.isArray(value)) throw new ContractError('assignments 必须是数组')
  return value.map(parseAssignment)
}

export function parseAddDeviceGroupMemberResponse(value: unknown): AddDeviceGroupMemberResponse {
  const body = record(value, 'add_device_group_member')
  return {
    member: parseDeviceGroupMember(body.member),
    assignments: parseAssignments(body.assignments),
  }
}

function parseDeviceGroupMember(value: unknown, label = 'member'): DeviceGroupMember {
  const member = record(value, label)
  const weight = numberValue(member.weight, `${label}.weight`)
  const priority = nonNegativeInteger(member.priority, `${label}.priority`)
  if (!Number.isInteger(weight) || weight < 0 || weight > 1000 || priority > 1000) throw new ContractError(`${label} 权重或优先级超出范围`)
  return {
    group_id: text(member.group_id, `${label}.group_id`),
    node_id: text(member.node_id, `${label}.node_id`),
    dial_host: optionalText(member.dial_host, `${label}.dial_host`),
    weight,
    priority,
    retired_at: nullableText(member.retired_at, `${label}.retired_at`),
    created_at: optionalText(member.created_at, `${label}.created_at`),
    updated_at: optionalText(member.updated_at, `${label}.updated_at`),
  }
}

export function parseDeviceGroupMembersResponse(value: unknown): DeviceGroupMembersResponse {
  const body = record(value, 'device_group_members')
  if (!Array.isArray(body.items)) throw new ContractError('device_group_members.items 必须是数组')
  return { items: body.items.map((item, index) => parseDeviceGroupMember(item, `device_group_members.items[${index}]`)) }
}

export function parseRetireDeviceGroupMemberResponse(value: unknown): RetireDeviceGroupMemberResponse {
  const body = record(value, 'retire_device_group_member')
  if (typeof body.replayed !== 'boolean') throw new ContractError('retire_device_group_member.replayed 必须是布尔值')
  const member = parseDeviceGroupMember(body.member)
  if (!member.retired_at) throw new ContractError('retire_device_group_member.member.retired_at 必须存在')
  return { member, assignments: parseAssignments(body.assignments), replayed: body.replayed }
}

export function parseUpdateDeviceGroupMemberWeightResponse(value: unknown): UpdateDeviceGroupMemberWeightResponse {
  const body = record(value, 'update_device_group_member_weight')
  if (typeof body.replayed !== 'boolean') throw new ContractError('update_device_group_member_weight.replayed 必须是布尔值')
  const member = parseDeviceGroupMember(body.member)
  if (member.retired_at) throw new ContractError('update_device_group_member_weight.member 不得已退役')
  return { member, assignments: parseAssignments(body.assignments), replayed: body.replayed }
}

export function parseGroupRevisionResponse(value: unknown): CreateGroupRevisionResponse {
  const body = record(value, 'create_group_revision')
  const generation = record(body.generation, 'generation')
  if (typeof body.replayed !== 'boolean') throw new ContractError('replayed 必须是布尔值')
  return {
    generation: {
      id: text(generation.id, 'generation.id'),
      group_id: text(generation.group_id, 'generation.group_id'),
      generation: numberValue(generation.generation, 'generation.generation'),
      engine: text(generation.engine, 'generation.engine'),
      config: unknownRecord(generation.config, 'generation.config'),
      config_hash: text(generation.config_hash, 'generation.config_hash'),
      created_by: optionalText(generation.created_by, 'generation.created_by'),
      created_at: optionalText(generation.created_at, 'generation.created_at'),
    },
    assignments: parseAssignments(body.assignments),
    replayed: body.replayed,
  }
}

function optionalNezhaServerId(value: unknown, name: string): number | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 1) throw new ContractError(`${name} 必须是正整数`)
  return value
}

export function parseEnrollmentToken(value: unknown): { id: string; name: string; token: string; expires_at: string; nezha_server_id?: number } {
  const body = record(value, 'enrollment_token')
  return {
    id: text(body.id, 'id'),
    name: text(body.name, 'name'),
    token: text(body.token, 'token'),
    expires_at: text(body.expires_at, 'expires_at'),
    nezha_server_id: optionalNezhaServerId(body.nezha_server_id, 'nezha_server_id'),
  }
}

export function parsePendingEnrollmentTokensResponse(value: unknown, groupId: string) {
  const body = record(value, 'pending_enrollment_tokens')
  if (!Array.isArray(body.items)) throw new ContractError('pending_enrollment_tokens.items 必须是数组')
  return { items: body.items.map((value, index) => {
    const label = `pending_enrollment_tokens.items[${index}]`
    const item = record(value, label)
    const itemGroupId = text(item.group_id, `${label}.group_id`)
    if (itemGroupId !== groupId) throw new ContractError(`${label}.group_id 与当前设备组不一致`)
    return {
      id: text(item.id, `${label}.id`),
      name: text(item.name, `${label}.name`),
      group_id: itemGroupId,
      expires_at: text(item.expires_at, `${label}.expires_at`),
      created_at: text(item.created_at, `${label}.created_at`),
      nezha_server_id: optionalNezhaServerId(item.nezha_server_id, `${label}.nezha_server_id`),
    }
  }) }
}

export function parseRevokeEnrollmentTokenResponse(value: unknown) {
  const body = record(value, 'revoke_enrollment_token')
  const token = record(body.token, 'revoke_enrollment_token.token')
  if (typeof body.replayed !== 'boolean') throw new ContractError('revoke_enrollment_token.replayed 必须是布尔值')
  return { token: { id: text(token.id, 'revoke_enrollment_token.token.id'), revoked_at: text(token.revoked_at, 'revoke_enrollment_token.token.revoked_at') }, replayed: body.replayed }
}
