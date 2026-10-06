import type { PortRange } from '@/api/business'

const portToken = /^(\d{1,5})(?:\s*[-~]\s*(\d{1,5}))?$/

export function formatPortRanges(ranges: PortRange[] | undefined, legacyStart = 0, legacyEnd = 0): string {
  const effective = ranges?.length ? ranges : legacyStart > 0 && legacyEnd >= legacyStart ? [{ start: legacyStart, end: legacyEnd }] : []
  return effective.map((item) => item.start === item.end ? String(item.start) : `${item.start}-${item.end}`).join(', ')
}

export function parsePortRanges(value: string): PortRange[] {
  const tokens = value.split(/[,;，；\n]+/).map((item) => item.trim()).filter(Boolean)
  if (!tokens.length) throw new Error('至少填写一个监听端口或端口范围')
  if (tokens.length > 32) throw new Error('端口范围最多填写 32 段')

  const ranges = tokens.map((token) => {
    const match = token.match(portToken)
    if (!match) throw new Error(`端口格式不正确：${token}`)
    const start = Number(match[1])
    const end = Number(match[2] ?? match[1])
    if (start < 1 || end > 65535 || end < start) throw new Error(`端口范围必须在 1 至 65535 之间：${token}`)
    return { start, end }
  }).sort((left, right) => left.start - right.start || left.end - right.end)

  return ranges.reduce<PortRange[]>((merged, item) => {
    const previous = merged.at(-1)
    if (!previous || item.start > previous.end + 1) {
      merged.push({ ...item })
    } else if (item.end > previous.end) {
      previous.end = item.end
    }
    return merged
  }, [])
}
