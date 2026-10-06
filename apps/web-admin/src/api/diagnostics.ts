import { HttpClient } from './http'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

export interface DiagnosticTarget {
  id: string
  label: string
  kind: 'hostname' | 'ip'
}

export interface DiagnosticResult {
  target_id: string
  action: 'ping' | 'dns'
  status: 'ok' | 'failed'
  checked_at: string
}

export const diagnosticsApi = {
  async targets(): Promise<DiagnosticTarget[]> {
    const response = await http.request<{ scope: string; items: DiagnosticTarget[] }>('/diagnostics/targets')
    if (response.scope !== 'control-plane-local' || !Array.isArray(response.items)) throw new Error('诊断目标数据无效')
    return response.items
  },
  async run(targetId: string, action: 'ping' | 'dns'): Promise<DiagnosticResult> {
    const response = await http.request<{ result: DiagnosticResult }>('/diagnostics/run', {
      method: 'POST', body: JSON.stringify({ target_id: targetId, action }),
    })
    if (!response.result || response.result.target_id !== targetId || response.result.action !== action) throw new Error('诊断结果数据无效')
    return response.result
  },
}
