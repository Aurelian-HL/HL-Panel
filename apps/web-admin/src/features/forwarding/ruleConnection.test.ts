import { describe, expect, it } from 'vitest'
import type { ForwardRule } from '@/api/business'
import { ruleConnectionPresentation } from './ruleConnection'

describe('rule connection freshness', () => {
  const rule = { status: 'active', paused: false, connection: { status: 'online', connections: 4, reason: 'established' } } as ForwardRule
  it('uses fresh per-rule TCP observations', () => {
    expect(ruleConnectionPresentation(rule, 0)).toMatchObject({ status: 'online', label: '在线' })
    expect(ruleConnectionPresentation(rule, 0).title).toContain('不是人数')
    expect(ruleConnectionPresentation({ ...rule, connection: { status: 'error', connections: 0, reason: 'listener_or_apply_failed' } }, 0).status).toBe('error')
  })
  it('expires stale observations and never revives inactive rules', () => {
    expect(ruleConnectionPresentation(rule, 3000).status).toBe('offline')
    expect(ruleConnectionPresentation({ ...rule, paused: true }, 0).status).toBe('offline')
    expect(ruleConnectionPresentation({ ...rule, status: 'quota_exhausted' }, 0).status).toBe('offline')
    expect(ruleConnectionPresentation({ ...rule, connection: undefined }, 0).status).toBe('offline')
  })
})
