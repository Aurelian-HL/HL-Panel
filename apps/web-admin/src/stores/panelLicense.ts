import { ref } from 'vue'
import { panelLicenseApi, type PanelLicenseStatus } from '@/api/panelLicense'

const status = ref<PanelLicenseStatus | null>(null)
const error = ref('')
let generation = 0
let pending: Promise<void> | undefined
async function refresh(): Promise<void> {
  if (pending) return pending
  const current = generation
  pending = (async () => {
    try { const result = await panelLicenseApi.status(); if (current === generation) { status.value = result; error.value = '' } }
    catch (cause) { if (current === generation) error.value = cause instanceof Error ? cause.message : '授权状态读取失败' }
    finally { if (current === generation) pending = undefined }
  })()
  return pending
}
export const panelLicenseStore = {
  status, error, refresh,
  generation: () => generation,
  apply: (value: PanelLicenseStatus, expectedGeneration: number) => {
    if (expectedGeneration !== generation) return false
    status.value = value; error.value = ''; return true
  },
  clear: () => { generation++; pending = undefined; status.value = null; error.value = '' },
}
