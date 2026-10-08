import { HttpClient } from './http'

export interface PanelUpdateTask {
  id: string
  target_version: string
  state: 'running' | 'succeeded' | 'failed'
  phase: string
  message: string
  backup_directory?: string
  created_at: string
  updated_at: string
}
export interface PanelUpdateStatus {
  available: boolean
  message?: string
  task: PanelUpdateTask | null
}
const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
export const panelUpdateStatus = (): Promise<PanelUpdateStatus> => http.request('/panel/update', { cache: 'no-store' })
export const startPanelUpdate = (version: string, password: string, id: string): Promise<PanelUpdateStatus> => http.request('/panel/update', {
  method: 'POST', headers: { 'Idempotency-Key': id }, body: JSON.stringify({ version, administrator_password: password }),
})
