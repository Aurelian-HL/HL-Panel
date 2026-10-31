import { HttpClient } from './http'

const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
export interface PanelLicenseStatus {
  enabled: boolean
  status: 'active' | 'expired' | 'revoked' | 'stale' | 'unactivated' | 'unconfigured' | 'upgrade_required'
  management_allowed: boolean
  auto_registration?: boolean
  redemption_pending?: boolean
  domain: string
  license_id?: string
  revision?: number
  tier?: 'free' | 'licensed'
  limits?: { device_groups: number; rules: number }
  usage?: { device_groups: number; rules: number }
  usage_unavailable?: boolean
  term?: { kind: string; months?: number; days?: number }
  expires_at: string | null
  checked_at?: string
  lease_valid_until?: string
  last_error?: string
  reminder_due: boolean
  renewal_url: string
}
export interface LicenseChallenge { dns_name: string; dns_value: string; expires_at: string; purpose?: 'free-registration' }
export interface LicenseShopProduct {
  id: string
  name: string
  term: { kind: string; months?: number; days?: number }
  price_label?: string
  purchase_url?: string
}
export const panelLicenseApi = {
  status: () => http.request<PanelLicenseStatus>('/license/status', { cache: 'no-store' }),
  sync: () => http.request<PanelLicenseStatus>('/license/sync', { method: 'POST' }),
  dismiss: () => http.request<PanelLicenseStatus>('/license/reminders/dismiss', { method: 'POST' }),
  prepare: (code: string) => http.request<LicenseChallenge>('/license/activation/prepare', { method: 'POST', body: JSON.stringify({ code }) }),
  registerFree: () => http.request<LicenseChallenge>('/license/free/prepare', { method: 'POST' }),
  commit: () => http.request<PanelLicenseStatus>('/license/activation/commit', { method: 'POST' }),
  shop: () => http.request<{ items: LicenseShopProduct[]; redemption_enabled: boolean; message?: string }>('/license/shop', { cache: 'no-store', timeoutMs: 15_000 }),
  redeem: (code: string) => http.request<PanelLicenseStatus>('/license/redemption', { method: 'POST', body: JSON.stringify({ code }), timeoutMs: 25_000 }),
  resumeRedemption: () => http.request<PanelLicenseStatus>('/license/redemption/resume', { method: 'POST', timeoutMs: 15_000 }),
}
