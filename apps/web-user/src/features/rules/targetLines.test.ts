import { describe, expect, it } from 'vitest'

import { formatTargetLines, parseTargetLines } from './targetLines'

describe('target lines', () => {
  it('parses domains and bracketed IPv6 into the API target shape', () => {
    expect(parseTargetLines(' example.test:443\n\n[2001:db8::1]:8443\n')).toEqual([
      { host: 'example.test', port: 443 },
      { host: '2001:db8::1', port: 8443 },
    ])
    expect(formatTargetLines([{ host: '2001:db8::1', port: 8443 }])).toBe('[2001:db8::1]:8443')
  })

  it.each(['example.test', 'example.test:0', 'example.test:65536', '2001:db8::1:443', '[invalid-ipv6]:443', 'example.test/path:443'])('rejects invalid target %s', (value) => {
    expect(() => parseTargetLines(value)).toThrow('第 1 行目标地址格式无效')
  })

  it('rejects an empty list and more than 32 targets', () => {
    expect(() => parseTargetLines(' \n ')).toThrow('请填写目标地址')
    expect(() => parseTargetLines(Array.from({ length: 33 }, () => 'example.test:443').join('\n'))).toThrow('最多填写 32 条')
  })
})
