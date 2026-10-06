import type { ForwardRule, ForwardRuleInput, IngressProtocol, IngressStatus } from '@/api/business'

export function byteAmount(value: number): string {
  if (!value) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toLocaleString('zh-CN', { maximumFractionDigits: 2 })} ${units[index]}`
}

export function localDateTime(iso: string | null): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function targetAddress(host: string, port: number): string {
  return `${host.includes(':') ? `[${host}]` : host}:${port}`
}

export function parseTargetAddresses(text: string, allowEmpty = false): Array<{ host: string; port: number }> {
  const lines = text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  if (lines.length === 0 && allowEmpty) return []
  if (lines.length < 1 || lines.length > 32) throw new Error('请填写 1 至 32 行目标地址')
  return lines.map((line, index) => {
    const matched = line.match(/^\[([^\]]+)\]:(\d+)$/) ?? line.match(/^([^:]+):(\d+)$/)
    const host = matched?.[1] ?? ''
    const port = Number(matched?.[2] ?? 0)
    if (!validTargetHost(host) || !Number.isInteger(port) || port < 1 || port > 65535) throw new Error(`第 ${index + 1} 行目标地址无效，请使用 IP:端口 或 [IPv6]:端口`)
    return { host, port }
  })
}

/**
 * VLESS Reality rules use the same compact upstream notation as NY imports:
 * host:port:username:password. Credentials are deliberately parsed only at
 * save time and are never folded into the ordinary target list.
 */
export interface VLESSSOCKS5Target {
  host: string
  port: number
  username: string
  password: string
}

export function parseVLESSSOCKS5Target(text: string, allowStoredCredentials = false): VLESSSOCKS5Target {
  const lines = text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  if (lines.length !== 1) throw new Error('VLESS 目标地址只能填写一行 IP:端口:用户名:密码')
  const parts = lines[0]!.split(':')
  if (allowStoredCredentials && parts.length === 2) {
    const host = parts[0]!.trim()
    const port = Number(parts[1]!.trim())
    if (!validTargetHost(host) || !Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error('VLESS 目标地址必须使用有效的 IP:端口格式')
    }
    return { host, port, username: '', password: '' }
  }
  if (parts.length !== 4) throw new Error('VLESS 目标地址必须使用 IP:端口:账号:密码格式')
  const host = parts[0]!.trim()
  const rawPort = parts[1]!.trim()
  const username = parts[2]!.trim()
  const password = parts[3]!.trim()
  const port = Number(rawPort)
  if (!validTargetHost(host) || !Number.isInteger(port) || port < 1 || port > 65535 || !username || !password) {
    throw new Error('VLESS 目标地址必须使用有效的 IP:端口:用户名:密码格式')
  }
  return { host, port, username, password }
}

export function ruleInput(rule: ForwardRule): ForwardRuleInput {
  const ingressProtocol = effectiveIngressProtocol(rule)
  return {
    name: rule.name, rule_group_id: rule.rule_group_id, entry_group_id: rule.entry_group_id,
    exit_group_id: rule.exit_group_id, egress_mode: rule.egress_mode, ingress_protocol: ingressProtocol,
    vless_flow: ingressProtocol === 'vless_reality' ? (rule.vless_flow || 'xtls-rprx-vision') : '',
    reality_server_name: ingressProtocol === 'vless_reality' ? (rule.reality_server_name || '') : '',
    reality_public_key: ingressProtocol === 'vless_reality' ? (rule.reality_public_key || '') : '',
    reality_short_id: ingressProtocol === 'vless_reality' ? (rule.reality_short_id || '') : '',
    reality_destination: ingressProtocol === 'vless_reality' ? (rule.reality_destination || '') : '',
    // VLESS rules in the compact NY-compatible workflow always use the
    // authenticated SOCKS5 landing endpoint. Keep old DIRECT records from
    // leaking back into the editor as an actionable option.
    vless_outbound_mode: ingressProtocol === 'vless_reality' ? 'SOCKS5' : 'DIRECT',
    vless_socks5_host: ingressProtocol === 'vless_reality' ? (rule.vless_socks5_host || '') : '',
    vless_socks5_port: ingressProtocol === 'vless_reality' ? (rule.vless_socks5_port || 0) : 0,
    vless_socks5_username: ingressProtocol === 'vless_reality' ? (rule.vless_socks5_username || '') : '',
    // Passwords are never returned by the API. An empty value during edit
    // means "keep the existing secret" for the same endpoint and username.
    vless_socks5_password: '',
    protocol: rule.protocol,
    listen_port: rule.listen_port, targets: rule.targets.map((item) => ({ ...item })),
    selection_policy: rule.selection_policy, accept_proxy_protocol: rule.accept_proxy_protocol ?? false,
    send_proxy_protocol: rule.send_proxy_protocol ?? 0, speed_limit_mbps: rule.speed_limit_mbps ?? 0,
    ip_limit: rule.ip_limit ?? 0, connection_limit: rule.connection_limit ?? 0,
    paused: rule.paused, description: rule.description, revision: rule.revision,
  }
}

/** Older NY records did not carry a separate customer-facing protocol. */
export function effectiveIngressProtocol(rule: Pick<ForwardRule, 'ingress_protocol' | 'protocol'>): IngressProtocol {
  return rule.ingress_protocol ?? (rule.protocol === 'udp' ? 'udp' : 'tcp')
}

export function ingressProtocolLabel(protocol: IngressProtocol): string {
  if (protocol === 'vless_reality') return 'VLESS + Reality + Vision'
  if (protocol === 'socks5') return 'NY SOCKS5'
  return protocol === 'udp' ? 'NY UDP' : 'NY TCP'
}

export function ingressStatusFor(rule: Pick<ForwardRule, 'ingress_protocol' | 'protocol' | 'vless_flow' | 'reality_server_name' | 'reality_public_key' | 'reality_short_id' | 'ingress_status'>): IngressStatus {
  if (rule.ingress_status === 'pending_reality_parameters') return rule.ingress_status
  if (effectiveIngressProtocol(rule) !== 'vless_reality') return rule.ingress_status ?? 'ready'
  const complete = rule.vless_flow === 'xtls-rprx-vision' && Boolean(rule.reality_server_name && rule.reality_public_key && rule.reality_short_id)
  return complete ? (rule.ingress_status ?? 'ready') : 'pending_reality_parameters'
}

/** Validate user-entered, non-secret Reality listener material. Empty values
 * are allowed so an operator can save a rule and finish it later; that rule
 * is surfaced as pending_reality_parameters and cannot be treated as ready. */
export function validateRealityParameters(input: Pick<ForwardRuleInput, 'ingress_protocol' | 'vless_flow' | 'reality_server_name' | 'reality_public_key' | 'reality_short_id' | 'reality_destination'>): string | null {
  if (input.ingress_protocol !== 'vless_reality') {
    if (input.vless_flow || input.reality_server_name || input.reality_public_key || input.reality_short_id || input.reality_destination) return 'Reality 参数只能用于 VLESS + Reality + Vision 接入'
    return null
  }
  if (input.vless_flow && input.vless_flow !== 'xtls-rprx-vision') return 'VLESS 流控必须使用 xtls-rprx-vision'
  if (input.reality_server_name && !validTargetHost(input.reality_server_name)) return 'Reality SNI/服务器名称格式无效，请填写域名或 IP'
  if (input.reality_public_key && !/^(?:[A-Za-z0-9_-]{43}|[A-Za-z0-9_-]{43}=)$/.test(input.reality_public_key)) return 'Reality 公钥格式无效，应为 32 字节 Base64URL 公钥'
  if (input.reality_short_id && (!/^[0-9a-fA-F]{2,16}$/.test(input.reality_short_id) || input.reality_short_id.length % 2 !== 0)) return 'Reality Short ID 必须是 2 至 16 位偶数长度十六进制字符'
  if (input.reality_destination) {
    const matched = input.reality_destination.match(/^\[([^\]]+)\]:(\d+)$/) ?? input.reality_destination.match(/^([^:]+):(\d+)$/)
    const port = Number(matched?.[2] ?? 0)
    const host = matched?.[1] ?? ''
    if (!matched || !validTargetHost(host) || !Number.isInteger(port) || port < 1 || port > 65535) return 'Reality Decoy 目标格式无效，请填写域名或 IP:端口'
  }
  return null
}

export function validTargetHost(host: string): boolean {
  if (!host || host.length > 253 || /[\s/@?#\[\]]/.test(host)) return false
  if (host.includes(':')) {
    try { return new URL(`http://[${host}]/`).hostname.startsWith('[') } catch { return false }
  }
  return host.replace(/\.$/, '').split('.').every((part) => /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(part))
}
