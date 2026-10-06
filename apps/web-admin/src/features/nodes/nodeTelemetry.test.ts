import { describe, expect, it } from 'vitest'

import type { EdgeNode } from '@/api'
import { formatResourceBytes, hasFreshTelemetry } from './nodeTelemetry'

const sampledAt = '2026-10-04T00:00:00Z'
const node: EdgeNode = {
  id: 'node-1', name: '测试节点', hostname: 'test-node', platform: 'linux', architecture: 'amd64',
  agent_version: '1.0.0', boot_id: 'boot-1', engine_versions: {},
  status: 'online',
  last_heartbeat_at: sampledAt,
  resources: { memory_alloc_bytes: 1048576 },
  capabilities: [], desired_generation: 0, applied_generation: 0,
  last_apply_status: '', last_apply_message: '', created_at: sampledAt,
}

describe('node telemetry', () => {
  it('expires readings after the control-plane heartbeat lease', () => {
    const now = Date.parse(sampledAt)
    expect(hasFreshTelemetry(node, now + 90_000)).toBe(true)
    expect(hasFreshTelemetry(node, now + 90_001)).toBe(false)
    expect(hasFreshTelemetry({ ...node, status: 'offline' }, now)).toBe(false)
    expect(hasFreshTelemetry({ ...node, last_heartbeat_at: null }, now)).toBe(false)
    expect(hasFreshTelemetry({ ...node, last_heartbeat_at: 'invalid' }, now)).toBe(false)
    expect(hasFreshTelemetry(node, now - 1)).toBe(false)
  })

  it('only formats valid Agent process memory samples', () => {
    expect(formatResourceBytes(node, 'memory_alloc_bytes')).toBe('1.0 MiB')
    expect(formatResourceBytes({ ...node, resources: { memory_alloc_bytes: -1 } }, 'memory_alloc_bytes')).toBe('未上报')
    expect(formatResourceBytes({ ...node, resources: { memory_alloc_bytes: '1048576' } }, 'memory_alloc_bytes')).toBe('未上报')
  })
})
