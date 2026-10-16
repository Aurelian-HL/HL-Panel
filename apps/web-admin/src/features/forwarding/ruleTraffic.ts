import { ApiError } from '@/api/http'
import { usageApi } from '@/api/usage'
import type { ForwardRule } from '@/api/business'
import { serverNow } from '@/lib/serverClock'

interface TrafficCallbacks {
  isCurrent: () => boolean
  onValue: (ruleId: string, bytes: number) => void
  onUnauthorized: () => void
}

/** Read ledger totals without blocking the rules or flooding the API. */
export async function loadRuleTraffic(rules: Pick<ForwardRule, 'id' | 'traffic_quota_monthly'>[], callbacks: TrafficCallbacks): Promise<void> {
  const pending = [...new Map(rules.map((rule) => [rule.id, rule])).values()]
  const now = new Date(serverNow())
  const from = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)).toISOString()
  const to = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1)).toISOString()
  let sessionExpired = false
  const current = () => !sessionExpired && callbacks.isCurrent()
  async function worker(): Promise<void> {
    while (pending.length && current()) {
      const rule = pending.shift()!
      try {
        const result = await usageApi.query({ scope: 'rule', scope_id: rule.id, page: 1, page_size: 1,
          ...(rule.traffic_quota_monthly ? { from, to } : {}),
        })
        const bytes = result.totals.charged_bytes + (result.imported_traffic?.total_bytes ?? 0)
        if (current() && Number.isFinite(bytes) && bytes >= 0) callbacks.onValue(rule.id, bytes)
      } catch (cause) {
        if (current() && cause instanceof ApiError && cause.status === 401) {
          sessionExpired = true
          callbacks.onUnauthorized()
        }
        // An unavailable total stays absent, so the inventory displays —.
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(4, pending.length) }, worker))
}
