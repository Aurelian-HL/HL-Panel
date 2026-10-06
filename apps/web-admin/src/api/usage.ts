import { HttpClient } from './http'
import type { EnforcementRevokeResponse, UsageQuery, UsageResponse } from './usageTypes'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

function queryString(query: UsageQuery): string {
  const values = new URLSearchParams()
  values.set('scope', query.scope)
  if (query.scope !== 'site' && query.scope_id) values.set('scope_id', query.scope_id)
  if (query.from) values.set('from', query.from)
  if (query.to) values.set('to', query.to)
  if (query.page) values.set('page', String(query.page))
  if (query.page_size) values.set('page_size', String(query.page_size))
  return values.toString()
}

function validateResponse(value: UsageResponse): UsageResponse {
  if (!value || !Array.isArray(value.items) || !value.totals || typeof value.total !== 'number') {
    throw new Error('服务返回的流量数据格式不正确，请刷新后重试')
  }
  return value
}

export const usageApi = {
  async query(query: UsageQuery): Promise<UsageResponse> {
    return validateResponse(await http.request<UsageResponse>(`/usage?${queryString(query)}`))
  },
  async revoke(decisionId: string, idempotencyKey: string): Promise<EnforcementRevokeResponse> {
    return http.request<EnforcementRevokeResponse>(`/usage/enforcement/${encodeURIComponent(decisionId)}/revoke`, {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
    })
  },
}

export type * from './usageTypes'
