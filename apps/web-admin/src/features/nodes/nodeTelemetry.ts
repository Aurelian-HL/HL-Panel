import type { EdgeNode } from '@/api'

const heartbeatLeaseMs = 90_000

export function hasFreshTelemetry(node: EdgeNode, now = Date.now()): boolean {
  if (!['online', 'syncing', 'failed'].includes(node.status) || !node.last_heartbeat_at) return false
  const sampledAt = Date.parse(node.last_heartbeat_at)
  return Number.isFinite(sampledAt) && sampledAt <= now && now - sampledAt <= heartbeatLeaseMs
}

export function resourceInteger(node: EdgeNode, key: string): number | null {
  const value = node.resources[key]
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

export function formatResourceBytes(node: EdgeNode, key: string): string {
  const value = resourceInteger(node, key)
  return value === null ? '未上报' : formatBytes(value)
}

export function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`
  return `${(value / (1024 ** 3)).toFixed(1)} GB`
}
