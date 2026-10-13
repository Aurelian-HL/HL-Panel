import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/http'
import { loadRuleTraffic } from './ruleTraffic'

const mocked = vi.hoisted(() => ({ query: vi.fn(), serverNow: vi.fn() }))
vi.mock('@/api/usage', () => ({ usageApi: { query: mocked.query } }))
vi.mock('@/lib/serverClock', () => ({ serverNow: mocked.serverNow }))

beforeEach(() => {
  mocked.query.mockReset()
  mocked.serverNow.mockReturnValue(Date.parse('2026-12-31T23:59:00Z'))
  mocked.query.mockResolvedValue({ totals: { charged_bytes: 42 } })
})

describe('loadRuleTraffic', () => {
  it('queries monthly fallback totals within the UTC month using server time and deduplicates rules', async () => {
    const onValue = vi.fn()
    await loadRuleTraffic([
      { id: 'monthly', traffic_quota_monthly: true },
      { id: 'lifetime' },
      { id: 'monthly', traffic_quota_monthly: true },
    ], { isCurrent: () => true, onValue, onUnauthorized: vi.fn() })
    expect(mocked.query.mock.calls.map(([query]) => query)).toEqual([
      { scope: 'rule', scope_id: 'monthly', page: 1, page_size: 1, from: '2026-12-01T00:00:00.000Z', to: '2027-01-01T00:00:00.000Z' },
      { scope: 'rule', scope_id: 'lifetime', page: 1, page_size: 1 },
    ])
    expect(onValue.mock.calls).toEqual([['monthly', 42], ['lifetime', 42]])
  })

  it('stops writing values and scheduling queued rules after session expiry', async () => {
    const onValue = vi.fn()
    const onUnauthorized = vi.fn()
    mocked.query.mockRejectedValueOnce(new ApiError('expired', 401))
    await loadRuleTraffic(Array.from({ length: 8 }, (_, i) => ({ id: String(i) })), {
      isCurrent: () => true, onValue, onUnauthorized,
    })
    expect(mocked.query).toHaveBeenCalledTimes(4)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(onValue).not.toHaveBeenCalled()
  })
})
