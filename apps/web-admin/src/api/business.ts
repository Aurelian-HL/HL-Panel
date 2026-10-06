import { HttpClient } from './http'
import type { Customer, CustomerInput, ForwardExportDocument, ForwardImportPreview, ForwardImportResult, ForwardRule, ForwardRuleInput, ForwardTransferRequest, ForwardingConnection, ForwardingTargetProbe, GroupNetwork, GroupNetworkInput, RuleBatchInput, RuleBatchResult, RuleGroup, RuleGroupInput, UserGroup, UserGroupInput } from './businessTypes'
import type { DeviceGroup } from './types'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

function list<T>(body: unknown): { items: T[] } {
  if (!body || typeof body !== 'object' || !Array.isArray((body as { items?: unknown }).items)) throw new Error('服务返回的数据格式不正确，请刷新后重试')
  return body as { items: T[] }
}
function write<T>(path: string, input: unknown, id: string | null, key: string): Promise<T> {
  return http.request<T>(id ? `${path}/${encodeURIComponent(id)}` : path, {
    method: id ? 'PUT' : 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(input),
  })
}

export const businessApi = {
  async deviceGroups() { return list<DeviceGroup>(await http.request('/device-groups')) },
  async customers() { return list<Customer>(await http.request('/customers')) },
  async saveCustomer(input: CustomerInput, id: string | null, key: string) { return (await write<{ customer: Customer }>('/customers', input, id, key)).customer },
  async userGroups() { return list<UserGroup>(await http.request('/user-groups')) },
  async saveUserGroup(input: UserGroupInput, id: string | null, key: string) { return (await write<{ user_group: UserGroup }>('/user-groups', input, id, key)).user_group },
  async rules() { return list<ForwardRule>(await http.request('/forwarding-rules')) },
  async forwardingConnection(id: string) { return http.request<ForwardingConnection>(`/forwarding-rules/${encodeURIComponent(id)}/connection`) },
  async saveRule(input: ForwardRuleInput, id: string | null, key: string) { return (await write<{ rule: ForwardRule }>('/forwarding-rules', input, id, key)).rule },
  async probeTarget(input: { host: string; port: number }) { return (await http.request<{ probe: ForwardingTargetProbe }>('/forwarding-target-probes', { method: 'POST', body: JSON.stringify(input) })).probe },
  async ruleGroups() { return list<RuleGroup>(await http.request('/rule-groups')) },
  async saveRuleGroup(input: RuleGroupInput, id: string | null, key: string) { return (await write<{ rule_group: RuleGroup }>('/rule-groups', input, id, key)).rule_group },
  async batchRules(input: RuleBatchInput, key: string) {
    return (await http.request<{ result: RuleBatchResult }>('/forwarding-rules/batch', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(input) })).result
  },
  async previewRuleImport(input: ForwardTransferRequest) {
    return (await http.request<{ preview: ForwardImportPreview }>('/forwarding-rules/import/preview', { method: 'POST', body: JSON.stringify(input) })).preview
  },
  async importRules(input: ForwardTransferRequest, key: string) {
    return (await http.request<{ result: ForwardImportResult }>('/forwarding-rules/import', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(input) })).result
  },
  async exportRules() { return http.request<ForwardExportDocument>('/forwarding-rules/export') },
  async groupNetworks() { return list<GroupNetwork>(await http.request('/group-networks')) },
  async saveGroupNetwork(input: GroupNetworkInput, groupId: string, key: string) {
    return (await http.request<{ network: GroupNetwork }>(`/group-networks/${encodeURIComponent(groupId)}`, { method: 'PUT', headers: { 'Idempotency-Key': key }, body: JSON.stringify(input) })).network
  },
}

export type * from './businessTypes'
