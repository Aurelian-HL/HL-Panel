export const probeRemarksStorageKey = 'hl_probe_remarks_v1'
export const probeRemarkMaxLength = 80
export type ProbeRemarks = Record<string, string>

export function loadProbeRemarks(): ProbeRemarks {
  const remarks: ProbeRemarks = Object.create(null)
  try {
    const value: unknown = JSON.parse(localStorage.getItem(probeRemarksStorageKey) ?? '{}')
    if (!value || typeof value !== 'object' || Array.isArray(value)) return remarks
    for (const [nodeId, remark] of Object.entries(value)) {
      if (typeof remark === 'string' && remark.trim() && remark.trim().length <= probeRemarkMaxLength) remarks[nodeId] = remark.trim()
    }
  } catch { /* Unavailable or damaged local preferences must not block monitoring. */ }
  return remarks
}

export function saveProbeRemark(nodeId: string, value: string): ProbeRemarks {
  const remark = value.trim()
  if (!nodeId || remark.length > probeRemarkMaxLength) throw new Error('备注最多 80 个字符。')
  // Merge with storage on each edit to preserve changes made in another tab.
  const remarks = loadProbeRemarks()
  if (remark) remarks[nodeId] = remark
  else delete remarks[nodeId]
  localStorage.setItem(probeRemarksStorageKey, JSON.stringify(remarks))
  return remarks
}
