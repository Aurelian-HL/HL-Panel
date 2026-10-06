import { describe, expect, it } from 'vitest'

import { completeProbeSample, unavailableProbeSample } from '@/test/fixtures/probe'
import { parseProbeGroupResponse } from './probe'

describe('probe response parsing', () => {
  it('accepts full Nezha readings and unlinked members without readings', () => {
    expect(parseProbeGroupResponse(completeProbeSample)).toEqual(completeProbeSample)
    expect(parseProbeGroupResponse(unavailableProbeSample)).toEqual(unavailableProbeSample)
  })

  it('accepts an unmapped Nezha server with live readings', () => {
    const inventory = { upstream_status: 'ok', items: [{ node_id: 'nezha:1', link_status: 'unmanaged', nezha_server_id: 1, online: true, name: 'Agent 1', cpu_percent: 4, sampled_at: '2026-10-04T08:00:00Z' }] }
    expect(parseProbeGroupResponse(inventory)).toMatchObject(inventory)
  })

  it('rejects malformed identities and measurements', () => {
    expect(() => parseProbeGroupResponse({ upstream_status: 'ok', items: [{ node_id: '', link_status: 'linked' }] })).toThrow('节点 ID')
    expect(() => parseProbeGroupResponse({ upstream_status: 'ok', items: [{ node_id: 'n1', link_status: 'linked', cpu_percent: -1 }] })).toThrow('CPU')
    expect(() => parseProbeGroupResponse({ upstream_status: 'cached', items: [] })).toThrow('上游状态')
    expect(() => parseProbeGroupResponse({ upstream_status: 'ok', items: [{ node_id: 'n1', link_status: 'linked', host_architecture: 1 }] })).toThrow('主机架构')
    expect(() => parseProbeGroupResponse({ upstream_status: 'ok', items: [{ node_id: 'n1', link_status: 'linked', tcp_conn_count: 1.5 }] })).toThrow('TCP 连接数')
    expect(() => parseProbeGroupResponse({ upstream_status: 'ok', items: [{ node_id: 'n1', link_status: 'linked', udp_conn_count: -1 }] })).toThrow('UDP 连接数')
  })
})
