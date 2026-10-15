import { HttpClient } from './http'

const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))

export interface LicenseView {
  id: string
  prefix: string
  domain: string
  months: number
  issued_at: string
  activated_at?: string
  expires_at?: string
  status: 'active' | 'revoked' | 'expired' | string
  bound: boolean
}

export interface LicenseLease {
  license_id: string
  instance_id: string
  domain: string
  activated_at: string
  expires_at: string
  checked_at: string
}

export interface LicenseStatus {
  items: LicenseView[]
  current?: LicenseLease
}

export interface LicenseRevokeResult {
  result: LicenseView
  replayed: boolean
}

export const licenseApi = {
  status(): Promise<LicenseStatus> {
    return http.request<LicenseStatus>('/license', { cache: 'no-store' })
  },
  issue(domain: string, months: number, idempotencyKey: string): Promise<{ code: string; license: LicenseView; replayed?: boolean }> {
    return http.request('/license/codes', {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify({ domain, months, idempotency_key: idempotencyKey }),
    })
  },
  revoke(id: string, idempotencyKey: string): Promise<LicenseRevokeResult> {
    return http.request<LicenseRevokeResult>(`/license/codes/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
    })
  },
}
