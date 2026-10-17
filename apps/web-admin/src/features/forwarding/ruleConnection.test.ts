import { describe, expect, it } from 'vitest'
import type { ForwardRule } from '@/api/business'
import { ruleConnectionPresentation } from './ruleConnection'

describe('rule connection evidence', () => {
  const rule = { status: 'active', paused: false, connection: { status: 'online', connections: 4, reason: 'established', valid_for_ms: 2300, sampled_at: '2026-10-17T00:00:00Z' } } as ForwardRule
  it('keeps established connections separate from user count and egress reachability', () => {
    const state = ruleConnectionPresentation(rule, 0)
    expect(state).toMatchObject({ status: 'online', label: '有连接' })
    expect(state.details.join(' ')).toContain('不是人数')
    expect(state.details.join(' ')).toContain('不代表出口可达')
    expect(state.details.join(' ')).toContain('节点采样时间')
  })
  it('distinguishes idle, unavailable, pending and unsupported telemetry from a broken rule', () => {
    for (const [reason, label] of [['no_connections', '空闲'], ['not_sampled', '待采样'], ['deployment_pending', '待配置确认'], ['tcp_only', '不适用'], ['sample_expired', '采样过期']] as const) {
      const state = ruleConnectionPresentation({ ...rule, connection: { status: 'offline', connections: 0, reason } }, 0)
      expect(state.label).toBe(label)
      expect(state.status).toBe('offline')
    }
    expect(ruleConnectionPresentation({ ...rule, connection: undefined }, 0).label).toBe('待采样')
  })
  it('expires at the remaining server lease, including a zero lease and re-fetched old data', () => {
    expect(ruleConnectionPresentation(rule, 2299).status).toBe('online')
    expect(ruleConnectionPresentation(rule, 2300).label).toBe('采样过期')
    expect(ruleConnectionPresentation({ ...rule, connection: { ...rule.connection!, valid_for_ms: 0 } }, 0).label).toBe('采样过期')
    const refetched = { ...rule, connection: { ...rule.connection!, valid_for_ms: 100 } }
    expect(ruleConnectionPresentation(refetched, 100).label).toBe('采样过期')
  })
  it('does not display stale failures as current failures', () => {
    const failed: ForwardRule = { ...rule, connection: { ...rule.connection!, status: 'error', connections: 0, reason: 'listener_missing', missing_listener_nodes: 1 } }
    expect(ruleConnectionPresentation(failed, 0).label).toBe('监听异常')
    expect(ruleConnectionPresentation(failed, 2300).label).toBe('采样过期')
  })
  it('preserves separate listener and apply failures, and partial failure warnings', () => {
    const failed: ForwardRule = { ...rule, connection: { ...rule.connection!, status: 'error', connections: 0, reason: 'node_apply_failed', apply_failed_nodes: 1, pending_nodes: 1 } }
    const state = ruleConnectionPresentation(failed, 0)
    expect(state.label).toBe('节点更新异常')
    expect(state.details.join(' ')).toContain('这不等于已有规则监听已失效')
    const partial = ruleConnectionPresentation({ ...rule, connection: { ...rule.connection!, error_nodes: 1, apply_failed_nodes: 1 } }, 0)
    expect(partial.status).toBe('error')
    expect(partial.label).toBe('有连接 · 部分异常')
    expect(partial.title).toContain('连接 4 条')
  })
  it('does not revive paused or disabled rules and supports old responses conservatively', () => {
    expect(ruleConnectionPresentation({ ...rule, paused: true }, 0).label).toBe('已暂停')
    expect(ruleConnectionPresentation({ ...rule, status: 'quota_exhausted' }, 0).label).toBe('未启用')
    const legacy: ForwardRule = { ...rule, connection: { status: 'error', connections: 0, reason: 'listener_or_apply_failed' } }
    expect(ruleConnectionPresentation(legacy, 0).label).toBe('采样异常')
    expect(ruleConnectionPresentation(legacy, 3000).label).toBe('采样过期')
  })
})
