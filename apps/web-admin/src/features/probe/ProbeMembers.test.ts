import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { completeProbeSample, unavailableProbeSample } from '@/test/fixtures/probe'
import ProbeMembers from './ProbeMembers.vue'

describe('ProbeMembers', () => {
  it('renders local flag images instead of platform-dependent emoji letters', () => {
    const items = [
      { ...completeProbeSample.items[0]!, country_code: 'US' },
      { ...completeProbeSample.items[1]!, country_code: 'HK' },
    ]
    const wrapper = mount(ProbeMembers, { props: { items, upstreamStatus: 'ok', now: Date.now() } })
    const rows = wrapper.findAll('.probe-table tbody tr')
    expect(rows[0]!.get('.probe-region img').attributes('src')).toBe('/flags/us.png')
    expect(rows[1]!.get('.probe-region img').attributes('src')).toBe('/flags/hk.png')
    expect(rows[0]!.get('.probe-region').text()).toBe('US')
    expect(rows[1]!.get('.probe-region').text()).toBe('HK')
    expect(wrapper.findAll('.probe-mobile-facts .probe-region img')).toHaveLength(2)
    wrapper.unmount()
  })

  it('shows complete host readings and keeps IP/region claims distinct from line egress', async () => {
    const wrapper = mount(ProbeMembers, { props: { items: completeProbeSample.items, upstreamStatus: 'ok', now: Date.now() }, attachTo: document.body })
    const row = wrapper.get('.probe-table tbody tr')
    expect(wrapper.findAll('.probe-table th')).toHaveLength(10)
    expect(row.text()).not.toContain('广州电信入口')
    expect(row.text()).not.toContain(completeProbeSample.items[0]!.node_id)
    expect(wrapper.get('.probe-mobile-item header').text()).toContain(completeProbeSample.items[0]!.ipv4)
    expect(wrapper.get('.probe-mobile-item header').text()).not.toContain('广州电信入口')
    expect(row.text()).toContain('8.4 Mbps')
    expect(row.text()).toContain('1.0 GiB')
    expect(row.text()).toContain('1 天 1 小时')
    expect(row.text()).toContain('37%')
    expect(row.text()).toContain('50%')
    expect(row.get('.probe-region').attributes('title')).toBe('主机 GeoIP：CN')
    expect(row.get('.probe-status').attributes('title')).toBeUndefined()
    expect(row.text()).not.toContain('最近采样')
    await row.get('.probe-status').trigger('mouseenter')
    expect(document.body.querySelector('.probe-status-tooltip')?.textContent).toContain('编号: 17')
    expect(document.body.querySelector('.probe-status-tooltip')?.textContent).toContain('OS: debian_13.2')
    expect(document.body.querySelector('.probe-status-tooltip')?.textContent).toContain('架构: x86_64')
    expect(document.body.querySelector('.probe-status-tooltip')?.textContent).toContain('节点端版本: v2.3.5')
    await row.get('.probe-status').trigger('mouseleave')
    expect(document.body.querySelector('.probe-status-tooltip')).toBeNull()
    await row.get('.probe-rate-hover').trigger('mouseenter')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('连接数: 2,113')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('TCP: 2,104')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('UDP: 9')
    await row.get('.probe-rate-hover').trigger('mouseleave')
    expect(document.body.querySelector('.probe-rate-tooltip')).toBeNull()
    expect(row.get('td:nth-child(5) span').attributes('title')).toContain('启动时间：')
    expect(row.findAll('td')).toHaveLength(10)
    const offlineRow = wrapper.findAll('.probe-table tbody tr')[1]!
    expect(offlineRow.text()).not.toContain('历史采样')
    expect(offlineRow.text()).not.toContain('98%')
    expect(offlineRow.text()).not.toContain('16.8 Mbps')
    await wrapper.get('[aria-label="查看广州电信入口监测详情"]').trigger('click')
    expect(document.body.textContent).toContain('主机 GeoIP 国家')
    expect(document.body.textContent).toContain('线路出口 IP')
    expect(document.body.textContent).toContain('未验证')
    expect(document.body.textContent).toContain('主机在线也不代表转发线路健康')
    wrapper.unmount()
  })

  it('does not invent status or metrics for unavailable and unlinked hosts', () => {
    const wrapper = mount(ProbeMembers, { props: { items: unavailableProbeSample.items, upstreamStatus: 'unavailable', now: Date.now() } })
    expect(wrapper.get('.probe-status').text()).toBe('未关联')
    expect(wrapper.get('.probe-table tbody').text()).toContain('未采集')
    const unavailableRow = wrapper.findAll('.probe-table tbody tr')[1]!
    expect(unavailableRow.text()).toContain('数据不可用')
    expect(unavailableRow.text()).not.toContain('历史采样')
    expect(unavailableRow.text()).not.toContain('90%')
    expect(unavailableRow.text()).not.toContain('8.4 Mbps')
    wrapper.unmount()
  })

  it('does not infer a total when only one connection protocol is sampled', async () => {
    const member = { ...completeProbeSample.items[0]!, udp_conn_count: undefined }
    const wrapper = mount(ProbeMembers, { props: { items: [member], upstreamStatus: 'ok', now: Date.now() }, attachTo: document.body })
    await wrapper.get('.probe-rate-hover').trigger('mouseenter')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('连接数: 未采集')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('TCP: 2,104')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('UDP: 未采集')
    wrapper.unmount()
  })

  it('expires live status, rates, and progress without waiting for a server response', async () => {
    const sampledAt = Date.parse(completeProbeSample.items[0]!.sampled_at!)
    const wrapper = mount(ProbeMembers, { props: { items: completeProbeSample.items, upstreamStatus: 'ok', now: sampledAt + 30_000 } })
    expect(wrapper.findAll('.probe-table tbody tr')[0]!.text()).toContain('在线')
    await wrapper.setProps({ now: sampledAt + 30_001 })
    const row = wrapper.findAll('.probe-table tbody tr')[0]!
    expect(row.text()).toContain('采样过期')
    expect(row.text()).not.toContain('历史采样')
    expect(row.text()).not.toContain('8.4 Mbps')
    expect(row.text()).not.toContain('37%')
    await row.get('.probe-rate-hover').trigger('focus')
    expect(document.body.querySelector('.probe-rate-tooltip')?.textContent).toContain('TCP: 未采集')
    await row.get('.probe-rate-hover').trigger('blur')
    wrapper.unmount()
  })
})
