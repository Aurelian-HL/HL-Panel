import { describe, expect, it } from 'vitest'
import { effectiveIngressProtocol, ingressProtocolLabel, ingressStatusFor, parseTargetAddresses, parseVLESSSOCKS5Target, targetAddress, validateRealityParameters } from './businessFormatters'
import { displayError } from './displayFormatters'

describe('forwarding target entry', () => {
  it('accepts multiple lines, blank lines, DNS and bracketed IPv6 without losing ports', () => {
    expect(parseTargetAddresses(' example.test:443\n\n[2001:db8::1]:8443\r\n192.0.2.10:80')).toEqual([
      { host: 'example.test', port: 443 }, { host: '2001:db8::1', port: 8443 }, { host: '192.0.2.10', port: 80 },
    ])
    expect(targetAddress('2001:db8::1', 8443)).toBe('[2001:db8::1]:8443')
  })
  it.each(['socks5://secret@example.test:443', '1.9.206.175:6019:username:password', 'example.test:0', '[2001:db8::1]:65536', 'example.test'])('rejects invalid target %s', (value) => {
    expect(() => parseTargetAddresses(value)).toThrow('目标地址无效')
  })
  it('only permits an empty target list when explicitly requested by dynamic VLESS SOCKS5', () => {
    expect(() => parseTargetAddresses('')).toThrow('请填写 1 至 32 行目标地址')
    expect(parseTargetAddresses('', true)).toEqual([])
  })
})

describe('compact VLESS SOCKS5 target', () => {
  it('parses the NY-compatible host:port:user:password form', () => {
    expect(parseVLESSSOCKS5Target('192.217.9.52:35556:1W1kCUxy:Cs1ENjkhqs')).toEqual({ host: '192.217.9.52', port: 35556, username: '1W1kCUxy', password: 'Cs1ENjkhqs' })
  })
  it.each(['192.217.9.52:0:user:pass', '192.217.9.52:35556::pass', '192.217.9.52:35556:user:', '192.217.9.52:35556:user:pass:extra'])('rejects malformed compact target %s', (value) => {
    expect(() => parseVLESSSOCKS5Target(value)).toThrow('VLESS 目标地址')
  })
})

describe('operator error messages', () => {
  it('explains revision and port conflicts in Chinese without raw internals', () => {
    expect(displayError(new Error('conflict: forwarding rule changed; reload before saving'))).toContain('刷新后重试')
    expect(displayError(new Error('conflict: listen port is already reserved for this protocol'))).toContain('监听端口不可用')
    expect(displayError(new Error('internal unrecognized detail'))).toBe('操作失败，请检查填写内容并重试')
  })
})

describe('VLESS Reality ingress', () => {
  it('keeps legacy TCP and UDP records readable while exposing pending Reality state', () => {
    expect(effectiveIngressProtocol({ protocol: 'tcp' })).toBe('tcp')
    expect(effectiveIngressProtocol({ protocol: 'udp' })).toBe('udp')
    expect(ingressProtocolLabel('tcp')).toBe('NY TCP')
    expect(ingressProtocolLabel('udp')).toBe('NY UDP')
    expect(ingressProtocolLabel('socks5')).toBe('NY SOCKS5')
    expect(ingressStatusFor({ protocol: 'tcp' })).toBe('ready')
    expect(ingressStatusFor({ ingress_protocol: 'vless_reality', protocol: 'tcp' })).toBe('pending_reality_parameters')
    expect(ingressStatusFor({ ingress_protocol: 'vless_reality', protocol: 'tcp', ingress_status: 'ready' })).toBe('pending_reality_parameters')
    expect(ingressStatusFor({ ingress_protocol: 'vless_reality', protocol: 'tcp', vless_flow: 'xtls-rprx-vision', reality_server_name: 'www.example.com', reality_public_key: 'AbCdEf0123456789AbCdEf0123456789AbCdEf01234', reality_short_id: '0123456789abcdef' })).toBe('ready')
  })

  it('allows staged Reality values but rejects malformed non-empty material', () => {
    expect(validateRealityParameters({ ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', reality_server_name: '', reality_public_key: '', reality_short_id: '' })).toBeNull()
    expect(validateRealityParameters({ ingress_protocol: 'vless_reality', reality_server_name: 'bad name', reality_public_key: '', reality_short_id: '' })).toContain('SNI')
    expect(validateRealityParameters({ ingress_protocol: 'vless_reality', reality_server_name: 'example.com', reality_public_key: 'short', reality_short_id: '' })).toContain('公钥')
    expect(validateRealityParameters({ ingress_protocol: 'vless_reality', reality_server_name: 'example.com', reality_public_key: '', reality_short_id: 'xyz' })).toContain('Short ID')
  })
})
