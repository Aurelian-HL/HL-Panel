import { HttpClient } from './http'
import type { Announcement, AnnouncementInput, PublicSiteInfo, SiteSettings, SiteSettingsInput } from './siteTypes'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

function items<T>(value: unknown): T[] {
  if (!value || typeof value !== 'object' || !Array.isArray((value as { items?: unknown }).items)) {
    throw new Error('服务返回的数据格式不正确，请刷新后重试')
  }
  return (value as { items: T[] }).items
}

export const siteApi = {
  async settings(): Promise<SiteSettings> {
    return (await http.request<{ settings: SiteSettings }>('/site-settings')).settings
  },
  async saveSettings(input: SiteSettingsInput, idempotencyKey: string): Promise<SiteSettings> {
    return (await http.request<{ settings: SiteSettings }>('/site-settings', {
      method: 'PUT', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input),
    })).settings
  },
  async announcements(): Promise<Announcement[]> {
    return items<Announcement>(await http.request('/announcements'))
  },
  async saveAnnouncement(input: AnnouncementInput, id: string | null, idempotencyKey: string): Promise<Announcement> {
    const suffix = id ? `/${encodeURIComponent(id)}` : ''
    return (await http.request<{ announcement: Announcement }>(`/announcements${suffix}`, {
      method: id ? 'PUT' : 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input),
    })).announcement
  },
  async publicInfo(): Promise<PublicSiteInfo> {
    return http.request<PublicSiteInfo>('/public/site-info')
  },
}

export type * from './siteTypes'
