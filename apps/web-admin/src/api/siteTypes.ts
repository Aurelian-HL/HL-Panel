export type SiteTheme = 'classic' | 'transparent'

export interface SiteSettingsInput {
  site_name: string
  panel_title: string
  public_description: string
  support_url: string
  theme: SiteTheme
  background_image_url: string
  revision: number
}

export interface SiteSettings extends SiteSettingsInput {
  id: string
  updated_at: string
}

export type AnnouncementLevel = 'info' | 'warning' | 'maintenance'

export interface AnnouncementInput {
  title: string
  content: string
  level: AnnouncementLevel
  enabled: boolean
  starts_at: string | null
  ends_at: string | null
  sort_order: number
  revision: number
}

export interface Announcement extends AnnouncementInput {
  id: string
  created_at: string
  updated_at: string
}

export interface PublicSiteInfo {
  settings: SiteSettings
  announcements: Announcement[]
  platform_version: string
  build_time: string | null
}
