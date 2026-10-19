import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { countryFlagCodes } from '../src/features/probe/countryFlags'

describe('bundled country flags', () => {
  it('keeps every supported country code paired with a usable local SVG', () => {
    const directory = resolve('public/flags')
    const files = readdirSync(directory).filter(name => name.endsWith('.svg')).sort()
    expect(files).toEqual([...countryFlagCodes].sort().map(code => `${code}.svg`))
    expect(countryFlagCodes.size).toBeGreaterThanOrEqual(249)
    for (const code of countryFlagCodes) {
      expect(code).toMatch(/^[a-z]{2}$/)
      const svg = readFileSync(join(directory, `${code}.svg`), 'utf8')
      expect(svg).toContain('<svg')
      expect(svg).not.toMatch(/<\s*(script|foreignObject)\b|\bon\w+\s*=/i)
    }
  })
})
