import { describe, expect, it } from 'vitest'

import { formatPortRanges, parsePortRanges } from './portRanges'

describe('port range editing', () => {
  it('accepts single ports and multiple separators, then canonicalizes overlaps', () => {
    expect(parsePortRanges('12000-12002, 12003；13000\n14000~14001')).toEqual([
      { start: 12000, end: 12003 },
      { start: 13000, end: 13000 },
      { start: 14000, end: 14001 },
    ])
  })

  it('formats new ranges and falls back to the legacy continuous range', () => {
    expect(formatPortRanges([{ start: 12000, end: 12000 }, { start: 13000, end: 13002 }])).toBe('12000, 13000-13002')
    expect(formatPortRanges(undefined, 20000, 21000)).toBe('20000-21000')
  })

  it.each(['', '0', '200-100', '65536', 'abc'])('rejects invalid input %s', (value) => {
    expect(() => parsePortRanges(value)).toThrow()
  })
})
