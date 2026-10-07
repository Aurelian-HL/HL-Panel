import type {
  AddDeviceGroupMemberRequest,
  AddDeviceGroupMemberResponse,
  CreateEndpointPoolRequest,
  CreateEndpointPoolResponse,
  DeleteEndpointPoolResponse,
  CreateDeviceGroupRequest,
  CreateGenerationRequest,
  DeviceGroup,
  DeviceGroupsResponse,
  DeviceGroupMember,
  DeviceGroupMembersResponse,
  EndpointPool,
  EndpointPoolsResponse,
  EdgeNode,
  EnrollmentTokenRequest,
  EnrollmentTokenResponse,
  PendingEnrollmentToken,
  PendingEnrollmentTokensResponse,
  RevokeEnrollmentTokenResponse,
  CreateGroupRevisionResponse,
  LoginRequest,
  LoginResponse,
  NodesResponse,
  OverviewResponse,
  RetireDeviceGroupMemberResponse,
} from './types'
import { ApiError } from './http'

const now = Date.now()

const nodes: EdgeNode[] = [
  {
    id: 'node_gz_01', name: '广州入口 01', hostname: 'gz-edge-01', platform: 'linux', architecture: 'amd64',
    agent_version: '0.1.0', boot_id: 'boot-gz-01', engine_versions: { xray: '25.9.11' }, resources: { cpu_percent: 18, memory_percent: 42 }, status: 'online', capabilities: ['xray', 'tcp'],
    desired_generation: 12, applied_generation: 12, last_apply_status: 'succeeded', last_apply_message: '',
    last_heartbeat_at: new Date(now - 12_000).toISOString(), created_at: new Date(now - 86400000 * 32).toISOString(),
  },
  {
    id: 'node_gz_02', name: '广州入口 02', hostname: 'gz-edge-02', platform: 'linux', architecture: 'amd64',
    agent_version: '0.1.0', boot_id: 'boot-gz-02', engine_versions: { xray: '25.9.11' }, resources: { cpu_percent: 22, memory_percent: 48 }, status: 'syncing', capabilities: ['xray', 'tcp'],
    desired_generation: 12, applied_generation: 11, last_apply_status: 'prepare', last_apply_message: '',
    last_heartbeat_at: new Date(now - 18_000).toISOString(), created_at: new Date(now - 86400000 * 28).toISOString(),
  },
  {
    id: 'node_hk_01', name: '香港节点 01', hostname: 'hk-edge-01', platform: 'linux', architecture: 'arm64',
    agent_version: '0.1.0', boot_id: 'boot-hk-01', engine_versions: { xray: '25.9.11' }, resources: { cpu_percent: 12, memory_percent: 39 }, status: 'failed', capabilities: ['xray', 'tcp'],
    desired_generation: 7, applied_generation: 6, last_apply_status: 'failed', last_apply_message: '配置校验失败',
    last_heartbeat_at: new Date(now - 42_000).toISOString(), created_at: new Date(now - 86400000 * 12).toISOString(),
  },
  {
    id: 'node_hk_02', name: '香港节点 02', hostname: 'hk-edge-02', platform: 'linux', architecture: 'amd64',
    agent_version: '0.1.0', boot_id: 'boot-hk-02', engine_versions: { xray: '25.9.11' }, resources: {}, status: 'offline', capabilities: ['xray', 'tcp'],
    desired_generation: 7, applied_generation: 7, last_apply_status: 'succeeded', last_apply_message: '',
    last_heartbeat_at: new Date(now - 7200_000).toISOString(), created_at: new Date(now - 86400000 * 9).toISOString(),
  },
]

const groups: DeviceGroup[] = [
  {
    id: 'group_gz_edge', name: '广州 VLESS 入口组', kind: 'ENTRY', selection_policy: 'weighted_least_connections', description: '国内入口直入直出设备组',
    member_count: 2, current_generation: 12, created_at: new Date(now - 86400000 * 30).toISOString(), updated_at: new Date(now - 3600_000).toISOString(),
  },
  {
    id: 'group_hk_edge', name: '香港出口组', kind: 'EXIT', selection_policy: 'weighted_round_robin', description: '香港出口与故障转移',
    member_count: 2, current_generation: 7, created_at: new Date(now - 86400000 * 11).toISOString(), updated_at: new Date(now - 7200_000).toISOString(),
  },
]

const endpointPools: EndpointPool[] = [
  {
    id: 'pool_gz_vless', name: '广州统一 VLESS 入口', group_id: 'group_gz_edge', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless',
    hostname: 'gz-entry.example.net', port: 443, selection_policy: 'weighted_least_connections', member_count: 2, healthy_candidate_count: 0,
    created_at: new Date(now - 86400000 * 18).toISOString(), updated_at: new Date(now - 42 * 60_000).toISOString(),
  },
]

const member = (groupId: string, nodeId: string, dialHost: string): DeviceGroupMember => ({
  group_id: groupId, node_id: nodeId, dial_host: dialHost, weight: 100, priority: 0,
  retired_at: null, created_at: new Date(now - 86400000).toISOString(), updated_at: new Date(now - 86400000).toISOString(),
})
const groupMembers = new Map<string, Map<string, DeviceGroupMember>>([
  ['group_gz_edge', new Map([
    ['node_gz_01', member('group_gz_edge', 'node_gz_01', 'gz-edge-01.example.test')],
    ['node_gz_02', member('group_gz_edge', 'node_gz_02', 'gz-edge-02.example.test')],
  ])],
  ['group_hk_edge', new Map([
    ['node_hk_01', member('group_hk_edge', 'node_hk_01', 'hk-edge-01.example.test')],
    ['node_hk_02', member('group_hk_edge', 'node_hk_02', 'hk-edge-02.example.test')],
  ])],
])
const groupRevisionRequests = new Map<string, { fingerprint: string; response: CreateGroupRevisionResponse }>()
const endpointPoolRequests = new Map<string, { fingerprint: string; response: CreateEndpointPoolResponse }>()
const endpointPoolDeletions = new Map<string, string>()
const pendingEnrollmentTokens = new Map<string, PendingEnrollmentToken>()

function pause<T>(value: T, milliseconds = 280): Promise<T> {
  return new Promise((resolve) => window.setTimeout(() => resolve(value), milliseconds))
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

export const mockApi = {
  async login(request: LoginRequest): Promise<LoginResponse> {
    if (request.username !== 'admin' || request.password !== 'demo123') throw new ApiError('账号或密码错误', 401, 'invalid_credentials')
    return pause({
      access_token: `demo_${crypto.randomUUID()}`,
      expires_at: new Date(Date.now() + 8 * 3600_000).toISOString(),
      user: { id: 'admin_demo', username: 'admin', display_name: '系统管理员', role: 'administrator' },
    })
  },
  getOverview(): Promise<OverviewResponse> {
    return pause({
      node_count: nodes.length,
      group_count: groups.length,
      online_node_count: nodes.filter((node) => node.status === 'online').length,
      syncing_node_count: nodes.filter((node) => node.status === 'syncing').length,
      failed_apply_count: nodes.filter((node) => node.status === 'failed').length,
      nodes: clone(nodes),
    })
  },
  getNodes(): Promise<NodesResponse> {
    return pause(clone({ items: nodes }))
  },
  getDeviceGroups(): Promise<DeviceGroupsResponse> {
    return pause(clone({ items: groups }))
  },
  getDeviceGroupMembers(groupId: string): Promise<DeviceGroupMembersResponse> {
    if (!groups.some((item) => item.id === groupId)) throw new ApiError('设备组不存在', 404, 'group_not_found')
    return pause(clone({ items: [...(groupMembers.get(groupId)?.values() ?? [])] }))
  },
  getEndpointPools(): Promise<EndpointPoolsResponse> {
    return pause(clone({ items: endpointPools }))
  },
  async createEnrollmentToken(request: EnrollmentTokenRequest): Promise<EnrollmentTokenResponse> {
    const issuedAt = new Date()
    const issued = {
      id: `enrollment_${crypto.randomUUID()}`,
      name: request.name,
      token: `enroll_${crypto.randomUUID().replaceAll('-', '')}`,
      expires_at: new Date(issuedAt.getTime() + request.expires_in_seconds * 1000).toISOString(),
    }
    if (request.group_id) pendingEnrollmentTokens.set(issued.id, { id: issued.id, name: issued.name, group_id: request.group_id, expires_at: issued.expires_at, created_at: issuedAt.toISOString() })
    return pause(issued)
  },
  getPendingGroupEnrollmentTokens(groupId: string): Promise<PendingEnrollmentTokensResponse> {
    if (!groups.some((item) => item.id === groupId)) throw new ApiError('设备组不存在', 404, 'group_not_found')
    return pause(clone({ items: [...pendingEnrollmentTokens.values()].filter((item) => item.group_id === groupId && Date.parse(item.expires_at) > Date.now()) }))
  },
  revokeEnrollmentToken(tokenId: string, _key: string): Promise<RevokeEnrollmentTokenResponse> {
    if (!pendingEnrollmentTokens.has(tokenId)) throw new ApiError('未使用令牌不存在', 404, 'token_not_found')
    pendingEnrollmentTokens.delete(tokenId)
    return pause({ token: { id: tokenId, revoked_at: new Date().toISOString() }, replayed: false })
  },
  async createDeviceGroup(request: CreateDeviceGroupRequest): Promise<DeviceGroup> {
    if (groups.some((group) => group.name === request.name)) throw new ApiError('设备组名称已存在', 409, 'group_name_conflict')
    const group: DeviceGroup = {
      id: `group_${crypto.randomUUID()}`,
      ...request,
      member_count: 0,
      current_generation: 0,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    groups.unshift(group)
    return pause(clone(group))
  },
  async deleteDeviceGroup(groupId: string, _key: string): Promise<{ replayed: boolean }> {
    const index = groups.findIndex((group) => group.id === groupId)
    if (index < 0) throw new ApiError('设备组不存在', 404, 'group_not_found')
    if (endpointPools.some((pool) => pool.group_id === groupId)) throw new ApiError('设备组仍被服务端点引用', 409, 'group_in_use')
    groups.splice(index, 1)
    groupMembers.delete(groupId)
    return pause({ replayed: false })
  },
  async addDeviceGroupMember(groupId: string, request: AddDeviceGroupMemberRequest): Promise<AddDeviceGroupMemberResponse> {
    const group = groups.find((item) => item.id === groupId)
    const node = nodes.find((item) => item.id === request.node_id)
    if (!group || !node) throw new ApiError('设备组或节点不存在', 404, 'not_found')
    const members = groupMembers.get(groupId) ?? new Map<string, DeviceGroupMember>()
    if (members.get(node.id)?.retired_at === null) throw new ApiError('节点已在设备组中', 409, 'member_exists')
    const updatedAt = new Date().toISOString()
    const addedMember: DeviceGroupMember = {
      group_id: groupId, node_id: node.id, dial_host: request.dial_host, weight: request.weight, priority: request.priority,
      retired_at: null, created_at: members.get(node.id)?.created_at ?? updatedAt, updated_at: updatedAt,
    }
    members.set(node.id, addedMember)
    groupMembers.set(groupId, members)
    group.member_count = [...members.values()].filter((item) => !item.retired_at).length
    group.updated_at = updatedAt
    for (const pool of endpointPools) {
      if (pool.group_id !== groupId) continue
      pool.member_count = group.member_count
      pool.updated_at = updatedAt
    }
    return pause(clone({
      member: addedMember,
      assignments: [],
    }))
  },
  async retireDeviceGroupMember(groupId: string, nodeId: string, _key: string): Promise<RetireDeviceGroupMemberResponse> {
    const group = groups.find((item) => item.id === groupId)
    const members = groupMembers.get(groupId)
    const target = members?.get(nodeId)
    if (!group || !target) throw new ApiError('设备组成员不存在', 404, 'member_not_found')
    if (target.retired_at) return pause(clone({ member: target, assignments: [], replayed: true }))
    const retiredAt = new Date().toISOString()
    target.retired_at = retiredAt
    target.updated_at = retiredAt
    group.member_count = [...members!.values()].filter((item) => !item.retired_at).length
    group.updated_at = retiredAt
    for (const pool of endpointPools) {
      if (pool.group_id !== groupId) continue
      pool.member_count = group.member_count
      pool.updated_at = retiredAt
    }
    return pause(clone({ member: target, assignments: [], replayed: false }))
  },
  async createGroupRevision(groupId: string, request: CreateGenerationRequest): Promise<CreateGroupRevisionResponse> {
    const group = groups.find((item) => item.id === groupId)
    if (!group) throw new ApiError('设备组不存在', 404, 'group_not_found')
    const requestKey = `${groupId}:${request.idempotency_key}`
    const fingerprint = JSON.stringify({ engine: request.engine, config: request.config })
    const previous = groupRevisionRequests.get(requestKey)
    if (previous) {
      if (previous.fingerprint !== fingerprint) throw new ApiError('幂等键已用于其他配置', 409, 'idempotency_conflict')
      return pause(clone({ ...previous.response, replayed: true }))
    }
    group.current_generation += 1
    group.updated_at = new Date().toISOString()
    const configHash = crypto.randomUUID().replaceAll('-', '').padEnd(64, '0').slice(0, 64)
    const members = [...(groupMembers.get(groupId)?.values() ?? [])].filter((item) => !item.retired_at).map((item) => item.node_id)
    const response: CreateGroupRevisionResponse = {
      generation: {
        id: `generation_${crypto.randomUUID()}`,
        group_id: groupId,
        generation: group.current_generation,
        engine: request.engine,
        config: request.config,
        config_hash: configHash,
        created_by: 'admin_demo',
        created_at: new Date().toISOString(),
      },
      assignments: members.map((nodeId) => ({
        id: `assignment_${crypto.randomUUID()}`,
        node_id: nodeId,
        generation: group.current_generation,
        engine: 'node-bundle',
        config: request.config,
        config_hash: configHash,
        created_at: new Date().toISOString(),
      })),
      replayed: false,
    }
    groupRevisionRequests.set(requestKey, { fingerprint, response: clone(response) })
    return pause(response)
  },
  async createEndpointPool(request: CreateEndpointPoolRequest): Promise<CreateEndpointPoolResponse> {
    const requestKey = request.idempotency_key
    const fingerprint = JSON.stringify({ ...request, idempotency_key: undefined })
    const previous = endpointPoolRequests.get(requestKey)
    if (previous) {
      if (previous.fingerprint !== fingerprint) throw new ApiError('幂等键已用于其他服务端点', 409, 'idempotency_conflict')
      return pause(clone({ ...previous.response, replayed: true }))
    }
    const group = groups.find((item) => item.id === request.group_id)
    if (!group) throw new ApiError('设备组不存在', 404, 'group_not_found')
    if (endpointPools.some((pool) => pool.group_id === request.group_id && pool.name === request.name)) {
      throw new ApiError('该设备组中已存在同名服务端点', 409, 'endpoint_pool_name_conflict')
    }
    if (endpointPools.some((pool) => pool.hostname === request.hostname && pool.port === request.port && pool.protocol === request.protocol)) {
      throw new ApiError('该服务端点已被占用', 409, 'stable_endpoint_conflict')
    }
    if (!Number.isInteger(request.port) || request.port < 1 || request.port > 65535) {
      throw new ApiError('端口必须在 1 至 65535 之间', 400, 'invalid_port')
    }
    const pool: EndpointPool = {
      id: `pool_${crypto.randomUUID()}`,
      name: request.name.trim(), group_id: request.group_id, rule_id: request.rule_id, mode: request.mode, protocol: request.protocol,
      hostname: request.hostname.trim(), port: request.port, selection_policy: request.selection_policy,
      member_count: group.member_count,
      // Node heartbeat is not a protocol health probe. New candidates remain
      // unverified until the endpoint probe path reports a successful check.
      healthy_candidate_count: 0,
      created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    }
    const response: CreateEndpointPoolResponse = { pool, replayed: false }
    endpointPools.unshift(pool)
    endpointPoolRequests.set(requestKey, { fingerprint, response: clone(response) })
    return pause(clone(response))
  },
  async deleteEndpointPool(poolId: string, key: string): Promise<DeleteEndpointPoolResponse> {
    const previousPoolId = endpointPoolDeletions.get(key)
    if (previousPoolId) {
      if (previousPoolId !== poolId) throw new ApiError('幂等键已用于其他服务端点', 409, 'idempotency_conflict')
      return pause({ replayed: true })
    }
    const index = endpointPools.findIndex((pool) => pool.id === poolId)
    if (index < 0) throw new ApiError('服务端点不存在', 404, 'endpoint_pool_not_found')
    endpointPools.splice(index, 1)
    endpointPoolDeletions.set(key, poolId)
    return pause({ replayed: false })
  },
}
