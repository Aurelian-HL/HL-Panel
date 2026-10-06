import type { RuleTarget } from '@/api/types'
import { target } from '@/lib/format'

export function formatTargetLines(targets: RuleTarget[]): string {
  return targets.map((item) => target(item.host, item.port)).join('\n')
}

export function parseTargetLines(value: string): RuleTarget[] {
  const lines = value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  if (!lines.length) throw new Error('请填写目标地址，每行一个 host:port')
  if (lines.length > 32) throw new Error('目标地址最多填写 32 条')

  return lines.map((line, index) => {
    const match = /^(\[[^\]\s]+\]|[^:\[\]\s]+):([0-9]{1,5})$/.exec(line)
    const port = match ? Number(match[2]) : 0
    if (!match || port < 1 || port > 65535) {
      throw new Error(`第 ${index + 1} 行目标地址格式无效，请填写 host:port 或 [IPv6]:port`)
    }
    try {
      const parsed = new URL(`http://${line}`)
      if (!parsed.hostname || parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash) {
        throw new Error('invalid target')
      }
    } catch {
      throw new Error(`第 ${index + 1} 行目标地址格式无效，请填写 host:port 或 [IPv6]:port`)
    }
    const host = match[1]!.startsWith('[') ? match[1]!.slice(1, -1) : match[1]!
    return { host, port }
  })
}
