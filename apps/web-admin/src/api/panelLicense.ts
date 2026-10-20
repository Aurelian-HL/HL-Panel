import { HttpClient } from './http'

const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
export interface PanelLicenseStatus {
  enabled: boolean
  status: 'active' | 'expired' | 'revoked' | 'stale' | 'unactivated' | 'unconfigured'
  management_allowed: boolean
  domain: string
  license_id?: string
  revision?: number
  term?: { kind: string; months?: number; days?: number }
  expires_at: string | null
  checked_at?: string
  lease_valid_until?: string
  last_error?: string
  reminder_due: boolean
  renewal_url: string
}
export interface LicenseChallenge { dns_name: string; dns_value: string; expires_at: string }
export const panelLicenseApi = {
  status: () => http.request<PanelLicenseStatus>('/license/status', { cache: 'no-store' }),
  sync: () => http.request<PanelLicenseStatus>('/license/sync', { method: 'POST' }),
  dismiss: () => http.request<PanelLicenseStatus>('/license/reminders/dismiss', { method: 'POST' }),
  prepare: (code: string) => http.request<LicenseChallenge>('/license/activation/prepare', { method: 'POST', body: JSON.stringify({ code }) }),
  commit: () => http.request<PanelLicenseStatus>('/license/activation/commit', { method: 'POST' }),
}
