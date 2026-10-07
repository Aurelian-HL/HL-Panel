import { HttpClient } from './http'
import type {
  AddDeviceGroupMemberRequest,
  AddDeviceGroupMemberResponse,
  CreateEndpointPoolRequest,
  CreateEndpointPoolResponse,
  DeleteEndpointPoolResponse,
  DeleteDeviceGroupResponse,
  CreateDeviceGroupRequest,
  UpdateDeviceGroupRequest,
  CreateGenerationRequest,
  DeviceGroup,
  DeviceGroupsResponse,
  DeviceGroupMembersResponse,
  EndpointPoolsResponse,
  EnrollmentTokenRequest,
  EnrollmentTokenResponse,
  PendingEnrollmentTokensResponse,
  RevokeEnrollmentTokenResponse,
  CreateGroupRevisionResponse,
  LoginRequest,
  LoginResponse,
  NodesResponse,
  OverviewResponse,
  NodeControlResult,
  PanelControlResult,
  RetireDeviceGroupMemberResponse,
  UpdateDeviceGroupMemberWeightResponse,
} from './types'
import {
  parseDeviceGroup,
  parseDeviceGroupsResponse,
  parseDeviceGroupMembersResponse,
  parseEndpointPoolResponse,
  parseDeleteEndpointPoolResponse,
  parseDeleteDeviceGroupResponse,
  parseEndpointPoolsResponse,
  parseAddDeviceGroupMemberResponse,
  parseRetireDeviceGroupMemberResponse,
  parseUpdateDeviceGroupMemberWeightResponse,
  parseEnrollmentToken,
  parsePendingEnrollmentTokensResponse,
  parseRevokeEnrollmentTokenResponse,
  parseGroupRevisionResponse,
  parseLoginResponse,
  parseNodesResponse,
  parseOverviewResponse,
  parseNodeControlResult,
  parsePanelControlResult,
} from './validators'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

interface AdminApi {
  login(request: LoginRequest): Promise<LoginResponse>
  getOverview(): Promise<OverviewResponse>
  getNodes(): Promise<NodesResponse>
  controlNode(nodeId: string, command: NodeControlResult['command']): Promise<NodeControlResult>
  getNodeControl(nodeId: string): Promise<NodeControlResult | null>
  controlPanel(command: PanelControlResult['command']): Promise<PanelControlResult>
  getPanelControl(): Promise<PanelControlResult | null>
  getDeviceGroups(): Promise<DeviceGroupsResponse>
  getDeviceGroupMembers(groupId: string): Promise<DeviceGroupMembersResponse>
  getEndpointPools(): Promise<EndpointPoolsResponse>
  createEnrollmentToken(request: EnrollmentTokenRequest): Promise<EnrollmentTokenResponse>
  getPendingGroupEnrollmentTokens(groupId: string): Promise<PendingEnrollmentTokensResponse>
  revokeEnrollmentToken(tokenId: string, key: string): Promise<RevokeEnrollmentTokenResponse>
  createDeviceGroup(request: CreateDeviceGroupRequest): Promise<DeviceGroup>
  updateDeviceGroup(groupId: string, request: UpdateDeviceGroupRequest, key: string): Promise<DeviceGroup>
  deleteDeviceGroup(groupId: string, key: string): Promise<DeleteDeviceGroupResponse>
  addDeviceGroupMember(groupId: string, request: AddDeviceGroupMemberRequest): Promise<AddDeviceGroupMemberResponse>
  retireDeviceGroupMember(groupId: string, nodeId: string, key: string): Promise<RetireDeviceGroupMemberResponse>
  updateDeviceGroupMemberWeight(groupId: string, nodeId: string, weight: number, updatedAt: string, key: string): Promise<UpdateDeviceGroupMemberWeightResponse>
  createGroupRevision(groupId: string, request: CreateGenerationRequest): Promise<CreateGroupRevisionResponse>
  createEndpointPool(request: CreateEndpointPoolRequest): Promise<CreateEndpointPoolResponse>
  deleteEndpointPool(poolId: string, key: string): Promise<DeleteEndpointPoolResponse>
}

const realApi: AdminApi = {
  async login(request) {
    return parseLoginResponse(await http.request('/auth/login', { method: 'POST', body: JSON.stringify(request) }))
  },
  async getOverview() {
    return parseOverviewResponse(await http.request('/overview'))
  },
  async getNodes() {
    return parseNodesResponse(await http.request('/nodes'))
  },
  async controlNode(nodeId, command) {
    const key = `${nodeId}:${command}:${crypto.randomUUID()}`
    const body = await http.request(`/nodes/${encodeURIComponent(nodeId)}/control`, { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ command }) })
    const value = (body as { control?: unknown }).control
    return parseNodeControlResult(value, 'control')
  },
  async getNodeControl(nodeId) {
    const value = await http.request<unknown>(`/nodes/${encodeURIComponent(nodeId)}/control`)
    return value === undefined ? null : parseNodeControlResult(value)
  },
  async controlPanel(command) {
    const body = await http.request('/panel/control', { method: 'POST', body: JSON.stringify({ command }) })
    return parsePanelControlResult(body, 'panel_control')
  },
  async getPanelControl() {
    const value = await http.request<unknown>('/panel/control')
    return value === undefined ? null : parsePanelControlResult(value)
  },
  async getDeviceGroups() {
    return parseDeviceGroupsResponse(await http.request('/device-groups'))
  },
  async getDeviceGroupMembers(groupId) {
    return parseDeviceGroupMembersResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/members`))
  },
  async getEndpointPools() {
    return parseEndpointPoolsResponse(await http.request('/endpoint-pools'))
  },
  async createEnrollmentToken(request) {
    return parseEnrollmentToken(await http.request('/enrollment-tokens', { method: 'POST', body: JSON.stringify(request) }))
  },
  async getPendingGroupEnrollmentTokens(groupId) {
    return parsePendingEnrollmentTokensResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/enrollment-tokens`), groupId)
  },
  async revokeEnrollmentToken(tokenId, key) {
    return parseRevokeEnrollmentTokenResponse(await http.request(`/enrollment-tokens/${encodeURIComponent(tokenId)}/revoke`, { method: 'POST', headers: { 'Idempotency-Key': key } }))
  },
  async createDeviceGroup(request) {
    return parseDeviceGroup(await http.request('/device-groups', { method: 'POST', body: JSON.stringify(request) }))
  },
  async updateDeviceGroup(groupId, request, key) {
    return parseDeviceGroup(await http.request(`/device-groups/${encodeURIComponent(groupId)}`, { method: 'PUT', headers: { 'Idempotency-Key': key }, body: JSON.stringify(request) }))
  },
  async deleteDeviceGroup(groupId, key) {
    return parseDeleteDeviceGroupResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}`, { method: 'DELETE', headers: { 'Idempotency-Key': key } }))
  },
  async addDeviceGroupMember(groupId, request) {
    return parseAddDeviceGroupMemberResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/members`, { method: 'POST', body: JSON.stringify(request) }))
  },
  async retireDeviceGroupMember(groupId, nodeId, key) {
    return parseRetireDeviceGroupMemberResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(nodeId)}/retire`, { method: 'POST', headers: { 'Idempotency-Key': key } }))
  },
  async updateDeviceGroupMemberWeight(groupId, nodeId, weight, updatedAt, key) {
    return parseUpdateDeviceGroupMemberWeightResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(nodeId)}/weight`, { method: 'PUT', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ weight, updated_at: updatedAt }) }))
  },
  async createGroupRevision(groupId, request) {
    return parseGroupRevisionResponse(await http.request(`/device-groups/${encodeURIComponent(groupId)}/generations`, { method: 'POST', body: JSON.stringify(request) }))
  },
  async createEndpointPool(request) {
    return parseEndpointPoolResponse(await http.request('/endpoint-pools', { method: 'POST', body: JSON.stringify(request) }))
  },
  async deleteEndpointPool(poolId, key) {
    return parseDeleteEndpointPoolResponse(await http.request(`/endpoint-pools/${encodeURIComponent(poolId)}`, { method: 'DELETE', headers: { 'Idempotency-Key': key } }))
  },
}

export const api: AdminApi = realApi

export * from './http'
export type * from './types'
