import { HttpClient } from './http'

export interface CurrentAdministrator {
  id: string
  username: string
  created_at: string
}

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

export const profileApi = {
  async current(): Promise<CurrentAdministrator> {
    const result = await http.request<{ user: CurrentAdministrator }>('/auth/me')
    return result.user
  },
  async changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await http.request('/auth/password', {
      method: 'PUT',
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    })
  },
}
