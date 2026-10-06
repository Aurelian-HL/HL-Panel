import { computed, readonly, ref } from 'vue'
import { siteApi } from '@/api/site'
import type { PublicSiteInfo } from '@/api/siteTypes'

const info = ref<PublicSiteInfo | null>(null)
let inFlight: Promise<PublicSiteInfo> | null = null
const BRAND_KEY = 'hl_panel_brand_v1'

function cachedBrand(): { site_name: string; panel_title: string } | null {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(BRAND_KEY) || 'null')
    if (value && typeof value === 'object' && 'site_name' in value && 'panel_title' in value &&
        typeof value.site_name === 'string' && typeof value.panel_title === 'string') {
      return { site_name: value.site_name, panel_title: value.panel_title }
    }
  } catch { /* Storage may be disabled. */ }
  return null
}

const brand = ref(cachedBrand())
const siteName = computed(() => info.value?.settings.site_name?.trim() || brand.value?.site_name?.trim() || 'HL-panel')
const panelTitle = computed(() => info.value?.settings.panel_title?.trim() || brand.value?.panel_title?.trim() || 'HL-panel')

async function load(force = false): Promise<PublicSiteInfo> {
  if (info.value && !force) return info.value
  if (inFlight && !force) return inFlight
  const request = siteApi.publicInfo().then((value) => {
    info.value = value
    brand.value = { site_name: value.settings.site_name, panel_title: value.settings.panel_title }
    try { sessionStorage.setItem(BRAND_KEY, JSON.stringify(brand.value)) } catch { /* Storage may be disabled. */ }
    return value
  }).finally(() => {
    if (inFlight === request) inFlight = null
  })
  inFlight = request
  return request
}

export const siteStore = {
  info: readonly(info),
  siteName: readonly(siteName),
  panelTitle: readonly(panelTitle),
  load,
  refresh: () => load(true),
}
