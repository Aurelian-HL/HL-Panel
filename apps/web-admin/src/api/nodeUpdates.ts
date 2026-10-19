import { HttpClient } from './http'

export interface NodeUpdateTask {
  id: string
  node_id: string
  target_version: string
  state: 'queued' | 'running' | 'verifying' | 'succeeded' | 'failed' | 'rolled_back' | 'cancelled' | 'uncertain'
  phase: string
  message: string
  backup_directory?: string
  updated_at: string
}
export interface NodeUpdateStatus {
  available: boolean
  message: string
  current_version: string
  panel_version: string
  versions: string[]
  task: NodeUpdateTask | null
}
const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
export const nodeUpdateStatus = (id: string): Promise<NodeUpdateStatus> => http.request(`/nodes/${encodeURIComponent(id)}/updates`, { cache: 'no-store' })
export const startNodeUpdate = (id: string, version: string, key: string): Promise<NodeUpdateTask> => http.request(`/nodes/${encodeURIComponent(id)}/updates`, {
  method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ version }),
})
export const cancelNodeUpdate = (id: string, task: string): Promise<NodeUpdateTask> => http.request(`/nodes/${encodeURIComponent(id)}/updates/${encodeURIComponent(task)}/cancel`, { method: 'POST', body: '{}' })
