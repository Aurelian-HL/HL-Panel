import { HttpClient } from './http'

export interface SubscriptionLine { name: string; binding_id?: string; uri?: string }
export interface Subscription {
  forwarding_rule_id?: string
  id: string; name: string; customer_id: string; state: 'active' | 'revoked'; revision: number
  published_revision: number; pending_update: boolean; line_count: number; published_line_count: number
  created_at: string; updated_at: string
}
export interface SubscriptionInput { name: string; customer_id: string; revision: number; lines: SubscriptionLine[] }
export interface SubscriptionDetail {
  subscription: Subscription; lines: SubscriptionLine[]
  preview: { name: string; uri?: string; error?: string }[]
  published_preview: { name: string; uri?: string; error?: string }[]
  links: { txt: string; yaml: string }; warning: string
}
const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
const path = (id: string) => `/subscriptions/${encodeURIComponent(id)}`
export const subscriptionApi = {
  generateRule: (id: string, key: string) => http.request<{ subscription: Subscription; replayed: boolean }>(`/forwarding-rules/${encodeURIComponent(id)}/subscription`, { method: 'POST', headers: { 'Idempotency-Key': key } }),
  list: () => http.request<{ items: Subscription[] }>('/subscriptions'),
  detail: (id: string) => http.request<SubscriptionDetail>(path(id)),
  save: (id: string | null, input: SubscriptionInput, key: string) => http.request<{ subscription: Subscription; replayed: boolean }>(id ? path(id) : '/subscriptions', { method: id ? 'PUT' : 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(input) }),
  action: (item: Subscription, action: 'publish' | 'rotate' | 'revoke' | 'restore', key: string) => http.request<{ subscription: Subscription }>(`${path(item.id)}/actions/${action}`, { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ revision: item.revision }) }),
  qrcode: (id: string, format: 'txt' | 'yaml') => http.request<{ png_base64: string }>(`${path(id)}/qrcode`, { method: 'POST', body: JSON.stringify({ base_url: location.origin, format }) }),
  package: (id: string) => http.request<{ filename: string; data_base64: string }>(`${path(id)}/package`, { method: 'POST', body: JSON.stringify({ base_url: location.origin }) }),
}
