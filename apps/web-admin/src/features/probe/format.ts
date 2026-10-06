import type { ProbeMember, ProbeUpstreamStatus } from '@/api/probe'

export function formatBytes(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value) || value < 0) return '未采集'
  if (value < 1024) return `${Math.round(value)} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let amount = value
  let unit = -1
  do { amount /= 1024; unit++ } while (amount >= 1024 && unit < units.length - 1)
  return `${amount.toFixed(1)} ${units[unit]}`
}

export function formatRate(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value) || value < 0) return '未采集'
  let bits = value * 8
  if (!Number.isFinite(bits)) return '未采集'
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps']
  let unit = 0
  while (bits >= 1000 && unit < units.length - 1) {
    bits /= 1000
    unit++
  }
  return unit === 0 ? `${Math.round(bits)} bps` : `${bits.toFixed(1)} ${units[unit]}`
}

export function formatUptime(seconds: number | undefined): string {
  if (seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return '未采集'
  const wholeMinutes = Math.floor(seconds / 60)
  const days = Math.floor(wholeMinutes / 1440)
  const hours = Math.floor((wholeMinutes % 1440) / 60)
  const minutes = wholeMinutes % 60
  if (days) return `${days} 天 ${hours} 小时`
  if (hours) return `${hours} 小时 ${minutes} 分`
  return `${minutes} 分`
}

export function countryFlag(code: string | undefined): string {
  const normalized = code?.toLowerCase()
  return normalized === 'us' || normalized === 'hk' ? `/flags/${normalized}.png` : ''
}

export function formatUnixTime(seconds: number | undefined): string {
  if (!seconds || !Number.isSafeInteger(seconds) || seconds > 8_640_000_000_000) return '未采集'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(seconds * 1000))
}

export function measuredPercent(value: number | undefined): number | undefined {
  return value !== undefined && Number.isFinite(value) && value >= 0 && value <= 100 ? Math.round(value) : undefined
}

export function capacityPercent(used: number | undefined, total: number | undefined): number | undefined {
  return used !== undefined && total !== undefined && Number.isFinite(used) && Number.isFinite(total) && used >= 0 && total > 0 && used <= total
    ? Math.round(used / total * 100)
    : undefined
}

export function hasFreshProbeSample(member: ProbeMember, now = Date.now()): boolean {
  if (!member.sampled_at) return false
  const sampledAt = Date.parse(member.sampled_at)
  return Number.isFinite(sampledAt) && sampledAt <= now + 5_000 && now - sampledAt <= 30_000
}

export function monitorStatus(member: ProbeMember, upstream: ProbeUpstreamStatus, now = Date.now()): { text: string; tone: string } {
  if (member.link_status === 'unlinked') return { text: '未关联', tone: 'muted' }
  if (upstream === 'disabled') return { text: '未配置', tone: 'muted' }
  if (upstream === 'unavailable') return { text: '数据不可用', tone: 'warning' }
  if (member.online === true && hasFreshProbeSample(member, now)) return { text: '在线', tone: 'success' }
  if (member.online === true && member.sampled_at) return { text: '采样过期', tone: 'warning' }
  if (member.online === false) return { text: '离线', tone: 'danger' }
  return { text: '未采集', tone: 'muted' }
}

export function hasLiveProbeReading(member: ProbeMember, upstream: ProbeUpstreamStatus, now = Date.now()): boolean {
  return upstream === 'ok' && member.link_status !== 'unlinked' && member.online === true && hasFreshProbeSample(member, now)
}

export function probeReadingState(member: ProbeMember, upstream: ProbeUpstreamStatus, now = Date.now()): string {
  if (upstream === 'unavailable' && member.link_status !== 'unlinked') return '数据不可用'
  if (member.sampled_at && !hasLiveProbeReading(member, upstream, now)) return '历史采样'
  return member.sampled_at ? '最近采样' : '尚无采样'
}
