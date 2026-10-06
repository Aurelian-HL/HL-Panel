import { describe, expect, it } from 'vitest'

import { mockApi } from './mock'

describe('mock endpoint pool projection', () => {
  it('projects later device-group members without treating node heartbeat as protocol health', async () => {
    const suffix = crypto.randomUUID()
    const group = await mockApi.createDeviceGroup({
      name: `投影测试-${suffix}`,
      kind: 'EDGE',
      selection_policy: 'rendezvous_hash',
      description: '',
    })
    const created = await mockApi.createEndpointPool({
      name: `稳定入口-${suffix}`,
      group_id: group.id,
      mode: 'SINGLE_SERVICE_ENDPOINT',
      protocol: 'vless',
      hostname: `${suffix}.example.test`,
      port: 443,
      selection_policy: group.selection_policy,
      idempotency_key: suffix,
    })
    expect(created.pool).toMatchObject({ member_count: 0, healthy_candidate_count: 0 })

    const added = await mockApi.addDeviceGroupMember(group.id, { node_id: 'node_gz_01', dial_host: 'edge-01.example.test', weight: 100, priority: 0 })
    expect(added.member.dial_host).toBe('edge-01.example.test')
    const projected = (await mockApi.getEndpointPools()).items.find((pool) => pool.id === created.pool.id)

    expect(projected).toMatchObject({ member_count: 1, healthy_candidate_count: 0 })
    expect(projected).not.toHaveProperty('member_uris')
  })

  it('lists and retires a member while preserving retirement replay and active counts', async () => {
    const group = await mockApi.createDeviceGroup({ name: `成员退役-${crypto.randomUUID()}`, kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '' })
    await mockApi.addDeviceGroupMember(group.id, { node_id: 'node_gz_01', dial_host: 'edge.example.test', weight: 20, priority: 1 })
    expect((await mockApi.getDeviceGroupMembers(group.id)).items).toMatchObject([{ node_id: 'node_gz_01', dial_host: 'edge.example.test', retired_at: null }])
    const first = await mockApi.retireDeviceGroupMember(group.id, 'node_gz_01', 'retire-key')
    expect(first).toMatchObject({ replayed: false, member: { node_id: 'node_gz_01' } })
    expect(first.member.retired_at).toBeTruthy()
    expect((await mockApi.retireDeviceGroupMember(group.id, 'node_gz_01', 'retire-key')).replayed).toBe(true)
    expect((await mockApi.getDeviceGroups()).items.find((item) => item.id === group.id)?.member_count).toBe(0)
  })

  it('deletes an endpoint pool idempotently', async () => {
    const key = crypto.randomUUID()
    const group = await mockApi.createDeviceGroup({ name: `删除测试-${key}`, kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '' })
    const created = await mockApi.createEndpointPool({ name: `待删除-${key}`, group_id: group.id, mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless', hostname: `${key}.example.test`, port: 443, selection_policy: group.selection_policy, idempotency_key: key })
    const operationKey = `delete-${key}`
    expect(await mockApi.deleteEndpointPool(created.pool.id, operationKey)).toEqual({ replayed: false })
    expect(await mockApi.deleteEndpointPool(created.pool.id, operationKey)).toEqual({ replayed: true })
    expect((await mockApi.getEndpointPools()).items.some((pool) => pool.id === created.pool.id)).toBe(false)
  })
})
