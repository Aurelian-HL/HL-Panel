import { readonly, ref } from 'vue'

import { customerApi, type PortalInfo } from '@/api'

const info = ref<PortalInfo | null>(null)
const loading = ref(false)
const error = ref('')
let pending: Promise<PortalInfo> | null = null

async function load(force = false): Promise<PortalInfo> {
  if (info.value && !force) return info.value
  if (pending) return pending

  loading.value = true
  error.value = ''
  pending = customerApi.portal()
    .then((value) => {
      info.value = value
      if (value.site_name) document.title = value.site_name
      return value
    })
    .catch((cause: unknown) => {
      error.value = cause instanceof Error ? cause.message : '站点信息加载失败'
      throw cause
    })
    .finally(() => {
      loading.value = false
      pending = null
    })

  return pending
}

export const customerSite = {
  info: readonly(info),
  loading: readonly(loading),
  error: readonly(error),
  load,
}
