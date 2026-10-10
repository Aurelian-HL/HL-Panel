import { describe, expect, it, vi } from 'vitest'

import { capacityPercent, countryFlag, formatBytes, formatRate, formatUnixTime, formatUptime, hasFreshProbeSample, hasLiveProbeReading, measuredPercent, monitorStatus } from './format'
import { completeProbeSample } from '@/test/fixtures/probe'
import { observeServerTime } from '@/lib/serverClock'

describe('probe measurement formatting', () => {
  it('does not expire fresh native samples when the browser clock is a day ahead', () => {
    const member = { ...completeProbeSample.items[0]!, source: 'hl' as const, link_status: 'native' as const }
    const sampledAt = Date.parse(member.sampled_at!)
    vi.spyOn(Date, 'now').mockReturnValue(sampledAt + 86_400_000)
    observeServerTime(new Date(sampledAt + 10_000).toISOString())
    expect(monitorStatus(member, 'disabled').text).toBe('在线')
    expect(hasLiveProbeReading(member, 'disabled')).toBe(true)
  })

  it('keeps native HL probes live without a Nezha upstream and expires after 90 seconds', () => {
    const member = { ...completeProbeSample.items[0]!, source: 'hl' as const, link_status: 'native' as const }
    const sampledAt = Date.parse(member.sampled_at!)
    for (const upstream of ['disabled', 'unavailable'] as const) {
      expect(hasLiveProbeReading(member, upstream, sampledAt + 90_000)).toBe(true)
      expect(monitorStatus(member, upstream, sampledAt + 90_000).text).toBe('在线')
      expect(hasLiveProbeReading(member, upstream, sampledAt + 90_001)).toBe(false)
      expect(monitorStatus(member, upstream, sampledAt + 90_001).text).toBe('采样过期')
      expect(monitorStatus({ ...member, online: false }, upstream, sampledAt).text).toBe('离线')
    }
  })
  it('keeps invalid and negative readings out of the interface', () => {
    for (const invalid of [-1, NaN, Infinity, -Infinity]) {
      expect(formatBytes(invalid)).toBe('未采集')
      expect(formatRate(invalid)).toBe('未采集')
      expect(formatUptime(invalid)).toBe('未采集')
      expect(measuredPercent(invalid)).toBeUndefined()
    }
    expect(capacityPercent(-1, 10)).toBeUndefined()
    expect(capacityPercent(1, Infinity)).toBeUndefined()
    expect(capacityPercent(11, 10)).toBeUndefined()
  })

  it('converts byte-per-second samples to decimal bit rates without changing traffic bytes', () => {
    expect(formatRate(0)).toBe('0 bps')
    expect(formatRate(125)).toBe('1.0 Kbps')
    expect(formatRate(1048576)).toBe('8.4 Mbps')
    expect(formatRate(125000000)).toBe('1.0 Gbps')
    expect(formatBytes(1073741824)).toBe('1.0 GB')
  })

  it('expires samples after 30 seconds and rejects invalid or future timestamps', () => {
    const member = completeProbeSample.items[0]!
    const sampledAt = Date.parse(member.sampled_at!)
    expect(hasFreshProbeSample(member, sampledAt + 30_000)).toBe(true)
    expect(hasLiveProbeReading(member, 'ok', sampledAt + 30_000)).toBe(true)
    expect(hasFreshProbeSample(member, sampledAt + 30_001)).toBe(false)
    expect(monitorStatus(member, 'ok', sampledAt + 30_001).text).toBe('采样过期')
    expect(hasFreshProbeSample(member, sampledAt - 5_001)).toBe(false)
    expect(hasFreshProbeSample({ ...member, sampled_at: 'invalid' }, sampledAt)).toBe(false)
    expect(hasFreshProbeSample({ ...member, sampled_at: undefined }, sampledAt)).toBe(false)
  })

  it('shows live samples for an unmapped Nezha server', () => {
    const member = { ...completeProbeSample.items[0]!, node_id: 'nezha:1', link_status: 'unmanaged' as const }
    const sampledAt = Date.parse(member.sampled_at!)
    expect(monitorStatus(member, 'ok', sampledAt).text).toBe('在线')
    expect(hasLiveProbeReading(member, 'ok', sampledAt)).toBe(true)
  })

  it('formats only valid country codes and Unix boot times', () => {
    expect(countryFlag('US')).toBe('/flags/us.png')
    expect(countryFlag('hk')).toBe('/flags/hk.png')
    expect(countryFlag('CN')).toBe('')
    expect(countryFlag('unknown')).toBe('')
    expect(formatUnixTime(1750000000)).not.toBe('未采集')
    expect(formatUnixTime(0)).toBe('未采集')
    expect(formatUnixTime(Number.MAX_SAFE_INTEGER)).toBe('未采集')
  })
})
