export function bytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let amount = value
  let index = 0
  while (amount >= 1024 && index < units.length - 1) { amount /= 1024; index++ }
  return `${amount.toFixed(index < 2 ? 0 : 2)} ${units[index]}`
}

export function dateTime(value: string | null | undefined): string {
  if (!value) return '永久'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '未知' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium', hour12: false }).format(date)
}

export function target(host: string, port: number): string {
  return host.includes(':') && !host.startsWith('[') ? `[${host}]:${port}` : `${host}:${port}`
}

export function ingressProtocolLabel(protocol: CustomerIngressProtocol | undefined, transport: string): string {
  if (protocol === 'vless_reality') return 'VLESS + Reality + Vision'
  if (protocol === 'socks5') return 'NY SOCKS5'
  if (protocol === 'udp' || transport.toLowerCase() === 'udp') return 'NY UDP'
  return 'NY TCP'
}

export type CustomerIngressProtocol = 'tcp' | 'udp' | 'socks5' | 'vless_reality'
