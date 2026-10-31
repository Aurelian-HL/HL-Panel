import type { ForwardRuleInput } from '@/api/business'
import { parseVLESSSOCKS5Target, type VLESSSOCKS5Target } from '@/lib/businessFormatters'

export const MAX_BATCH_CREATE = 500
export interface BatchCreateRow extends VLESSSOCKS5Target { line: number; name: string }
export interface BatchCreateIssue { line: number; message: string }

// Never include credentials in validation errors.
export function parseBatchCreate(text: string): { rows: BatchCreateRow[]; issues: BatchCreateIssue[] } {
  const rows: BatchCreateRow[] = []
  const issues: BatchCreateIssue[] = []
  let count = 0
  for (const [index, original] of text.split(/\r?\n/).entries()) {
    const line = original.trim()
    if (!line) continue
    if (++count > MAX_BATCH_CREATE) {
      issues.push({ line: index + 1, message: '每批最多创建 ' + MAX_BATCH_CREATE + ' 条，请分批提交' })
      break
    }
    const match = /^(?:(.*?)\s+)?(\S+)$/.exec(line)
    try {
      const target = parseVLESSSOCKS5Target(match?.[2] ?? '')
      const name = match?.[1]?.trim() || target.host
      if ([...name].length > 128) throw new Error('昵称过长')
      rows.push({ ...target, name, line: index + 1 })
    } catch {
      issues.push({ line: index + 1, message: '请填写“IP:端口:账号:密码”，昵称放在前面并以空格或制表符分隔；昵称不超过 128 字' })
    }
  }
  return { rows, issues }
}

export function batchCreateRule(row: BatchCreateRow, common: ForwardRuleInput): ForwardRuleInput {
  return { ...common, name: row.name, targets: [], vless_socks5_host: row.host, vless_socks5_port: row.port,
    vless_socks5_username: row.username, vless_socks5_password: row.password }
}
