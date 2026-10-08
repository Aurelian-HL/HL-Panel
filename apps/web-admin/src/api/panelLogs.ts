import { HttpClient } from './http'

export interface PanelLog { time: string; level: string; message: string }
export interface PanelLogs { items: PanelLog[]; persistent: boolean; updated_at: string }
const http = new HttpClient((import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, ''), () => sessionStorage.getItem('ny_admin_access_token'))
export async function getPanelLogs(limit: number): Promise<PanelLogs> {
  const value = await http.request<PanelLogs>(`/panel/logs?limit=${limit}`)
  if (!Array.isArray(value.items) || value.items.some(item => typeof item.time !== 'string' || typeof item.level !== 'string' || typeof item.message !== 'string')) throw new Error('日志返回格式错误，请重试')
  return value
}
