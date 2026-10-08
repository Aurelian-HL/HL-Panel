import { HttpClient } from './http'

export interface VersionStatus {
  current_version: string
  latest_version: string
  versions_behind: number
  versions_behind_at_least: boolean
  state: 'up_to_date' | 'update_available' | 'attention_required' | 'check_failed' | 'unpublished'
  attention_required: boolean
  can_defer: boolean
  checked_at: string
  message: string
  versions?: { tag: string; published_at: string; current: boolean; can_update: boolean; release_url: string }[]
}

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api/v1').replace(/\/$/, '')
const http = new HttpClient(baseUrl, () => sessionStorage.getItem('ny_admin_access_token'))

export async function checkVersion(): Promise<VersionStatus> {
  return http.request<VersionStatus>('/system/version')
}

export const repositoryUrl = 'https://github.com/Aurelian-HL/HL-Panel'
export const updateCommand = 'curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/update.sh | bash'

export function canDeferVersion(value: VersionStatus): boolean {
  return value.state === 'update_available' && value.can_defer === true && value.versions_behind >= 1 && value.versions_behind <= 2
}
