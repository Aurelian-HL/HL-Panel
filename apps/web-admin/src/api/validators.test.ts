import { describe, expect, it } from 'vitest'

import {
  ContractError,
  parseAddDeviceGroupMemberResponse,
  parseDeviceGroupsResponse,
  parseDeviceGroupMembersResponse,
  parseEndpointPoolResponse,
  parseEndpointPoolsResponse,
  parseEnrollmentToken,
  parseGroupRevisionResponse,
  parseNodesResponse,
  parseOverviewResponse,
  parsePendingEnrollmentTokensResponse,
  parseRevokeEnrollmentTokenResponse,
  parseRetireDeviceGroupMemberResponse,
} from './validators'

describe('API contract validators', () => {
  it('rejects cross-group pending tokens and drops secret fields', () => {
    const item = { id: 'token-1', name: '节点注册', group_id: 'entry-1', nezha_server_id: 2, expires_at: '2026-10-04T08:00:00Z', created_at: '2026-10-04T07:00:00Z', token: 'do-not-return' }
    expect(parsePendingEnrollmentTokensResponse({ items: [item] }, 'entry-1').items[0]).not.toHaveProperty('token')
    expect(parsePendingEnrollmentTokensResponse({ items: [item] }, 'entry-1').items[0]?.nezha_server_id).toBe(2)
    expect(() => parsePendingEnrollmentTokensResponse({ items: [item] }, 'exit-1')).toThrow(ContractError)
    expect(() => parsePendingEnrollmentTokensResponse({ items: [{ ...item, nezha_server_id: 0 }] }, 'entry-1')).toThrow(ContractError)
    expect(parseEnrollmentToken({ id: 'token-1', name: '节点注册', token: 'once', expires_at: item.expires_at, nezha_server_id: 2 }).nezha_server_id).toBe(2)
    expect(() => parseEnrollmentToken({ id: 'token-1', name: '节点注册', token: 'once', expires_at: item.expires_at, nezha_server_id: 1.5 })).toThrow(ContractError)
    expect(parseRevokeEnrollmentTokenResponse({ token: { id: 'token-1', revoked_at: '2026-10-04T07:30:00Z', token: 'secret' }, replayed: false }).token).not.toHaveProperty('token')
  })
  it('parses the confirmed overview envelope', () => {
    expect(parseOverviewResponse({
      node_count: 4,
      group_count: 2,
      online_node_count: 2,
      syncing_node_count: 1,
      failed_apply_count: 1,
    })).toEqual({
      node_count: 4,
      group_count: 2,
      online_node_count: 2,
      syncing_node_count: 1,
      failed_apply_count: 1,
    })
  })

  it('keeps only non-secret node inventory fields', () => {
    const parsed = parseNodesResponse({
      items: [{
        id: 'node-1',
        name: 'edge-1',
        hostname: 'edge-1.local',
        platform: 'linux',
        architecture: 'amd64',
        agent_version: '0.1.0',
        capabilities: ['xray'],
        status: 'ONLINE',
        boot_id: 'boot-1',
        engine_versions: { xray: '25.9.11' },
        resources: { cpu_percent: 12 },
        desired_generation: 3,
        applied_generation: 2,
        last_apply_status: 'failed',
        last_heartbeat_at: '2026-10-02T00:00:00Z',
        created_at: '2026-10-01T00:00:00Z',
        node_credential: 'must-not-be-exposed',
      }],
    })
    expect(parsed.items[0]?.status).toBe('online')
    expect(parsed.items[0]).not.toHaveProperty('node_credential')
    expect(parsed.items[0]?.engine_versions).toEqual({ xray: '25.9.11' })
  })

  it('uses GroupRevision fields separately from node desired generation', () => {
    const groups = parseDeviceGroupsResponse({ items: [{
      id: 'group-1', name: '广州边缘', kind: 'EDGE', description: '', member_count: 3,
      current_generation: 8, selection_policy: 'rendezvous_hash', created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-02T00:00:00Z',
    }] })
    expect(groups.items[0]?.current_generation).toBe(8)
    expect(groups.items[0]?.selection_policy).toBe('rendezvous_hash')

    const revision = parseGroupRevisionResponse({
      generation: {
        id: 'revision-9', group_id: 'group-1', generation: 9, engine: 'xray',
        config: { schema_version: 1, services: [] }, config_hash: 'a'.repeat(64), created_by: 'admin-1', created_at: '2026-10-02T00:00:00Z',
      },
      assignments: [{
        id: 'assignment-1', node_id: 'node-1', generation: 12, engine: 'node-bundle',
        config: { schema_version: 1, services: [] }, config_hash: 'b'.repeat(64), created_at: '2026-10-02T00:00:00Z',
      }],
      replayed: false,
    })
    expect(revision.generation.generation).toBe(9)
    expect(revision.assignments[0]?.generation).toBe(12)
  })

  it('rejects a response that drifts from snake_case numeric counts', () => {
    expect(() => parseOverviewResponse({ nodeCount: 2 })).toThrow(ContractError)
  })

  it('parses one stable endpoint without accepting member URI data', () => {
    const response = parseEndpointPoolsResponse({ items: [{
      id: 'pool-1', name: '广州入口', group_id: 'group-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless',
      hostname: 'entry.example.com', port: 443, selection_policy: 'weighted_least_connections',
      member_count: 10, healthy_candidate_count: 9, created_at: '2026-10-02T00:00:00Z', updated_at: '2026-10-02T00:01:00Z',
      member_uris: ['vless://must-not-be-forwarded'],
    }] })
    expect(response.items[0]).not.toHaveProperty('member_uris')
    expect(response.items[0]?.healthy_candidate_count).toBe(9)
    expect(() => parseEndpointPoolsResponse({ items: [{
      id: 'pool-1', name: '广州入口', group_id: 'group-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless',
      hostname: 'entry.example.com', port: 443, selection_policy: 'weighted_least_connections', member_count: 10,
      created_at: '2026-10-02T00:00:00Z', updated_at: '2026-10-02T00:01:00Z',
    }] })).toThrow(ContractError)
  })

  it('requires a boolean replay flag on endpoint creation', () => {
    expect(parseEndpointPoolResponse({
      pool: {
        id: 'pool-1', name: '广州入口', group_id: 'group-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'socks5', hostname: 'entry.example.com', port: 1080,
        selection_policy: 'weighted_round_robin', member_count: 2, healthy_candidate_count: 2, created_at: '', updated_at: '',
      }, replayed: false,
    }).pool.protocol).toBe('socks5')
    expect(() => parseEndpointPoolResponse({ pool: {}, replayed: 'false' })).toThrow(ContractError)
  })

  it('keeps the member dial host from the control-plane response', () => {
    const result = parseAddDeviceGroupMemberResponse({
      member: { group_id: 'entry-1', node_id: 'node-1', dial_host: 'edge.example.test', weight: 100, priority: 0 },
      assignments: [],
    })
    expect(result.member.dial_host).toBe('edge.example.test')
    expect(() => parseAddDeviceGroupMemberResponse({
      member: { group_id: 'entry-1', node_id: 'node-1', dial_host: 123, weight: 100, priority: 0 },
      assignments: [],
    })).toThrow(ContractError)
  })

  it('validates member inventory and retirement without forwarding unknown fields', () => {
    const member = { group_id: 'entry-1', node_id: 'node-1', dial_host: 'edge.example.test', weight: 100, priority: 0, retired_at: '2026-10-03T00:00:00Z' }
    const listed = parseDeviceGroupMembersResponse({ items: [{ ...member, secret: 'do-not-forward' }] })
    expect(listed.items[0]).toMatchObject(member)
    expect(listed.items[0]).not.toHaveProperty('secret')
    expect(parseRetireDeviceGroupMemberResponse({ member, assignments: [], replayed: false }).member.retired_at).toBe(member.retired_at)
    expect(() => parseDeviceGroupMembersResponse({ items: [{ ...member, weight: -1 }] })).toThrow(ContractError)
    expect(() => parseRetireDeviceGroupMemberResponse({ member: { ...member, retired_at: null }, assignments: [], replayed: false })).toThrow(ContractError)
    expect(() => parseRetireDeviceGroupMemberResponse({ member, assignments: [], replayed: 'false' })).toThrow(ContractError)
  })
})
