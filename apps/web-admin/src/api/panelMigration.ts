import { ApiError } from './http'

export interface MigrationPreview {
  manifest: { format: number; version: string; created_at: string; source_url: string }
  digest: string
  counts: Record<string, number>
}
export interface MigrationResult { recovery_id: string; replayed: boolean; restored_at: string }
const base = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')

async function request(path: string, init: RequestInit): Promise<Response> {
  const headers = new Headers(init.headers)
  const token = sessionStorage.getItem('ny_admin_access_token')
  if (token) headers.set('Authorization', `Bearer ${token}`)
  let response: Response
  try { response = await fetch(`${base}/panel/migration/${path}`, { ...init, headers, cache: 'no-store' }) }
  catch { throw new ApiError('网络连接失败，请检查面板连接；导入中断后先重新登录核对结果，勿反复覆盖') }
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const message = body.error?.message || body.message || `请求失败 (${response.status})`
    throw new ApiError(message, response.status)
  }
  return response
}
export async function exportMigration(administratorPassword: string, password: string): Promise<Blob> {
  return (await request('export', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ administrator_password: administratorPassword, password, source_url: window.location.origin }) })).blob()
}
function upload(file: File, administratorPassword: string, password: string): FormData {
  const data = new FormData()
  data.set('file', file); data.set('administrator_password', administratorPassword); data.set('password', password)
  return data
}
export async function previewMigration(file: File, administratorPassword: string, password: string): Promise<MigrationPreview> {
  return (await request('preview', { method: 'POST', body: upload(file, administratorPassword, password) })).json()
}
export async function importMigration(file: File, administratorPassword: string, password: string, digest: string, key: string): Promise<MigrationResult> {
  const data = upload(file, administratorPassword, password)
  data.set('digest', digest); data.set('confirm', 'RESTORE'); data.set('target_url', window.location.origin)
  return (await request('import', { method: 'POST', headers: { 'Idempotency-Key': key }, body: data })).json()
}
export async function recoveryMigration(id: string): Promise<Blob> {
  if (!/^mbk_[a-f0-9]{32}$/.test(id)) throw new Error('恢复备份编号无效')
  return (await request(`recovery/${id}`, { method: 'GET' })).blob()
}
export function downloadMigration(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a'); a.href = url; a.download = name; a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
