import { HttpClient } from './http'
import type { CustomerProfile, CustomerRule, CustomerRuleBatchResult, CustomerRuleDetail, CustomerRuleInput, CustomerRuleOptions, LoginResult, PortalInfo, UsageInfo } from './types'

const TOKEN_KEY = 'ny_customer_access_token'
const client = new HttpClient(() => sessionStorage.getItem(TOKEN_KEY))

function mutationKey(): string {
  return typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export const customerApi = {
  login: (username: string, password: string) => client.request<LoginResult>('/api/v1/customer/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  logout: () => client.request<void>('/api/v1/customer/auth/logout', { method: 'POST' }),
  portal: async () => (await client.request<{ portal: PortalInfo }>('/api/v1/customer/portal')).portal,
  me: async () => (await client.request<{ user: CustomerProfile }>('/api/v1/customer/me')).user,
  rules: async () => (await client.request<{ items: CustomerRule[] }>('/api/v1/customer/rules')).items,
  ruleOptions: () => client.request<CustomerRuleOptions>('/api/v1/customer/rule-options'),
  rule: async (id: string) => (await client.request<{ rule: CustomerRuleDetail }>(`/api/v1/customer/rules/${encodeURIComponent(id)}`)).rule,
  saveRule: async (input: CustomerRuleInput, id: string | null) => (await client.request<{ rule: CustomerRuleDetail }>(id ? `/api/v1/customer/rules/${encodeURIComponent(id)}` : '/api/v1/customer/rules', {
    method: id ? 'PUT' : 'POST', headers: { 'Idempotency-Key': mutationKey() }, body: JSON.stringify(input),
  })).rule,
  batchRules: async (operation: 'pause' | 'resume' | 'delete', rule: CustomerRuleDetail) => (await client.request<{ result: CustomerRuleBatchResult }>('/api/v1/customer/rules/batch', {
    method: 'POST', headers: { 'Idempotency-Key': mutationKey() }, body: JSON.stringify({ operation, rule_ids: [rule.id], expected_revisions: { [rule.id]: rule.revision } }),
  })).result,
  usage: async () => (await client.request<{ usage: UsageInfo }>('/api/v1/customer/usage')).usage,
  changePassword: (currentPassword: string, newPassword: string) => client.request<{ replayed: boolean }>('/api/v1/customer/password', {
    method: 'PUT', headers: { 'Idempotency-Key': mutationKey() }, body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  }),
}

export { TOKEN_KEY }
export * from './http'
export type * from './types'
