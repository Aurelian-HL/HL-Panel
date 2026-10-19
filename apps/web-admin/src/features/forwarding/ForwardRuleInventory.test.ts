import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import type { DeviceGroup } from '@/api'
import type { ForwardRule, GroupNetwork } from '@/api/business'
import ForwardRuleInventory from './ForwardRuleInventory.vue'

const devices: DeviceGroup[] = [
  { id: 'entry-1', name: '广州入口', kind: 'ENTRY', description: '', member_count: 2, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' },
  { id: 'exit-1', name: '香港出口', kind: 'EXIT', description: '', member_count: 2, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' },
]
const baseRule: ForwardRule = { id: 'direct-1', name: '入口规则', customer_id: 'legacy-owner', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
const networks: GroupNetwork[] = [
  { group_id: 'entry-1', connect_host: 'entry.example.test', port_start: 10000, port_end: 20000, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: ['exit-1'], traffic_multiplier: 1, revision: 1 },
  { group_id: 'exit-1', connect_host: 'exit.example.test', port_start: 10000, port_end: 20000, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 2, revision: 1 },
]

describe('ForwardRuleInventory', () => {
  it('uses the NY columns and separates real entry and exit addresses', () => {
    const exitRule: ForwardRule = { ...baseRule, id: 'exit-rule', name: '出口规则', egress_mode: 'EXIT_GROUP', exit_group_id: 'exit-1', targets: [{ host: '198.51.100.20', port: 8443 }] }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [baseRule, exitRule], devices, networks, ruleGroups: [], selectedIds: [], busyId: '' } })
    const text = wrapper.text()
    expect(wrapper.findAll('thead th').map((item) => item.text())).toEqual(['', '规则名', '入口', '出口', '已用流量', '状态', '操作'])
    expect(wrapper.find('thead input').attributes('aria-label')).toBe('选择全部规则')
    expect(wrapper.find('.rule-entry-cell').text()).toContain('入口：广州入口')
    expect(wrapper.find('.rule-entry-cell').text()).toContain('entry.example.test:10001')
    expect(wrapper.find('.rule-exit-cell').text()).toContain('出口：组内直出')
    expect(wrapper.find('.rule-exit-cell').text()).toContain('192.0.2.10:443')
    expect(wrapper.findAll('.rule-exit-cell')[1]!.text()).toContain('出口：香港出口')
    expect(wrapper.findAll('.rule-exit-cell')[1]!.text()).toContain('198.51.100.20:8443')
    expect(wrapper.findAll('.rule-exit-cell')[1]!.text()).toContain('倍率 2')
    expect(text).not.toContain('规则分组')
    expect(text).not.toContain('完整链路')
    expect(text).not.toContain('legacy-owner')
    wrapper.unmount()
  })

  it('renders real traffic totals and does not invent a zero when unavailable', () => {
    const missingRule: ForwardRule = { ...baseRule, id: 'missing' }
    const zeroRule: ForwardRule = { ...baseRule, id: 'zero' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [baseRule, missingRule, zeroRule], devices, ruleGroups: [], selectedIds: [], busyId: '', trafficByRule: { [baseRule.id]: 1024 ** 3 * 12.5, zero: 0 } } })
    expect(wrapper.findAll('td.rule-used-traffic').map((item) => item.text())).toEqual(['12.5 GB不限量', '—不限量', '0 B不限量'])
    wrapper.unmount()
  })

  it('does not show an operator-specific pre-migration traffic line in the release UI', () => {
    const imported: ForwardRule = { ...baseRule, imported_traffic: { upload_bytes: 1024, download_bytes: 2048 } }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [imported], devices, ruleGroups: [], selectedIds: [], busyId: '', trafficByRule: { [imported.id]: 3072 } } })
    expect(wrapper.text()).not.toContain('含迁移前')
    expect(wrapper.find('.rule-used-traffic').text()).toContain('3 KB')
    wrapper.unmount()
  })

  it('uses compact, labelled actions and preserves every rule action', async () => {
    const rule: ForwardRule = { ...baseRule, ingress_protocol: 'vless_reality' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    const actions = wrapper.find('.business-desktop .rule-actions')
    expect(actions.text()).toBe('')
    expect(actions.findAll('button').map((button) => button.attributes('title'))).toEqual(['编辑规则', '复制规则', '生成订阅并下载导入包', '暂停规则', '删除规则'])
    await actions.find('button[title="编辑规则"]').trigger('click')
    await actions.find('button[title="复制规则"]').trigger('click')
    await actions.find('button[title="生成订阅并下载导入包"]').trigger('click')
    await actions.find('button[title="暂停规则"]').trigger('click')
    await actions.find('button[title="删除规则"]').trigger('click')
    for (const event of ['edit', 'copy', 'connection', 'toggle', 'delete']) expect(wrapper.emitted(event)?.[0]).toEqual([rule])
    wrapper.unmount()
  })

  it('selects visible rules from the header and disables selections during updates', async () => {
    const secondRule: ForwardRule = { ...baseRule, id: 'second-rule' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [baseRule, secondRule], devices, ruleGroups: [], selectedIds: [baseRule.id], busyId: '' } })
    expect((wrapper.find('thead input').element as HTMLInputElement).indeterminate).toBe(true)
    await wrapper.find('thead input').setValue(true)
    expect(wrapper.emitted('select')).toEqual([[baseRule, true], [secondRule, true]])
    await wrapper.setProps({ busyId: baseRule.id })
    expect(wrapper.findAll('input[type="checkbox"]').every((checkbox) => checkbox.attributes('disabled') !== undefined)).toBe(true)
    expect(wrapper.findAll('.rule-actions button').every((button) => button.attributes('disabled') !== undefined)).toBe(true)
    wrapper.unmount()
  })

  it('opens listener diagnostics and keeps a zero-connection listener gray on both layouts', async () => {
    const rule: ForwardRule = { ...baseRule, status: 'active', connection: { status: 'error', connections: 0, reason: 'listener_missing', error_nodes: 1, sampled_nodes: 1, missing_listener_nodes: 1, sampled_at: '2026-10-17T00:00:00Z', valid_for_ms: 5000 } }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    for (const layout of ['.business-desktop', '.business-mobile']) {
      const badge = wrapper.get(layout + ' .rule-connection')
      expect(badge.text()).toContain('监听异常')
      expect(badge.attributes('aria-expanded')).toBe('false')
      await badge.trigger('click')
      expect(badge.attributes('aria-expanded')).toBe('true')
      const details = wrapper.get(layout + ' .rule-connection-detail').text()
      expect(details).toContain('未检测到监听端口：1 台节点')
      expect(details).toContain('节点采样时间')
      expect(details).toContain('不等于所有节点或客户端路径都失败')
      await badge.trigger('click')
      expect(wrapper.find(layout + ' .rule-connection-detail').exists()).toBe(false)
    }
    await wrapper.setProps({ rules: [{ ...rule, connection: { status: 'offline', connections: 0, reason: 'no_connections', valid_for_ms: 5000 } }] })
    for (const layout of ['.business-desktop', '.business-mobile']) {
      const badge = wrapper.get(layout + ' .rule-connection')
      expect(badge.text()).toContain('不在线')
      expect(badge.classes()).toContain('is-offline')
      expect(badge.attributes('title')).toContain('不代表线路不通')
    }
    wrapper.unmount()
  })

  it('keeps working connections green and exposes partial failures on both layouts', async () => {
    const rule: ForwardRule = { ...baseRule, status: 'active', connection: { status: 'online', connections: 4, reason: 'established', error_nodes: 2, sampled_nodes: 2, missing_listener_nodes: 1, apply_failed_nodes: 1, pending_nodes: 1, valid_for_ms: 5000 } }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    for (const layout of ['.business-desktop', '.business-mobile']) {
      const badge = wrapper.get(layout + ' .rule-connection')
      expect(badge.text()).toBe('在线+')
      expect(badge.classes()).toContain('is-online')
      expect(badge.classes()).not.toContain('is-error')
      await badge.trigger('click')
      const details = wrapper.get(layout + ' .rule-connection-detail').text()
      expect(details).toContain('当前 TCP 连接 4 条')
      expect(details).toContain('未检测到监听端口：1 台节点')
      expect(details).toContain('节点最新配置更新失败：1 台')
      expect(details).toContain('尚未确认当前规则配置：1 台节点')
    }
    wrapper.unmount()
  })

  it.each([
    ['not_sampled', '待采样'],
    ['deployment_pending', '待配置确认'],
    ['sample_expired', '采样过期'],
  ] as const)('keeps %s telemetry unknown without claiming the line is broken', (reason, label) => {
    const rule: ForwardRule = { ...baseRule, status: 'active', connection: { status: 'offline', connections: 0, reason } }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    for (const layout of ['.business-desktop', '.business-mobile']) {
      const badge = wrapper.get(layout + ' .rule-connection')
      expect(badge.text()).toBe(label + '+')
      expect(badge.classes()).toContain('is-offline')
      expect(badge.text()).not.toContain('故障')
    }
    wrapper.unmount()
  })

  it('shows VLESS Reality transport layers and its pending parameter state', () => {
    const vlessRule: ForwardRule = { ...baseRule, id: 'vless-rule', name: 'Reality 规则', ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', vless_flow: 'xtls-rprx-vision', ingress_status: 'pending_reality_parameters' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [vlessRule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    expect(wrapper.text()).toContain('VLESS + Reality / TCP · 目标 TCP+UDP')
    expect(wrapper.text()).toContain('待补 Reality 参数')
    wrapper.unmount()
  })

  it('does not present a legacy VLESS UDP classification as an enablement switch', () => {
    const vlessRule: ForwardRule = { ...baseRule, id: 'vless-udp-rule', name: 'Reality UDP 规则', ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', protocol: 'udp', vless_flow: '' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [vlessRule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    expect(wrapper.text()).toContain('VLESS + Reality / TCP · 目标 TCP+UDP')
    expect(wrapper.text()).not.toContain('解密后 UDP')
    wrapper.unmount()
  })

  it('shows only the VLESS upstream endpoint in the target column', () => {
    const vlessRule: ForwardRule = {
      ...baseRule,
      id: 'vless-upstream',
      name: '带认证上游',
      ingress_protocol: 'vless_reality',
      vless_flow: 'xtls-rprx-vision',
      vless_socks5_host: '192.217.9.52',
      vless_socks5_port: 35556,
      vless_socks5_username: 'upstream-user',
    }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [vlessRule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    const text = wrapper.text()
    expect(text).toContain('192.217.9.52:35556')
    expect(text).not.toContain('upstream-user')
    expect(text).not.toContain('upstream-password')
    wrapper.unmount()
  })

  it('does not present a VLESS rule as activated while runtime material is pending', () => {
    const vlessRule: ForwardRule = { ...baseRule, id: 'vless-runtime-pending', name: '等待节点材料', ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', reality_server_name: 'www.example.com', reality_public_key: 'AbCdEf0123456789AbCdEf0123456789AbCdEf01234', reality_short_id: '0123456789abcdef', ingress_status: 'ready', activation_reason: 'vless_runtime_material_pending' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [vlessRule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    expect(wrapper.text()).toContain('等待节点运行材料')
    expect(wrapper.text()).not.toContain('可用')
    wrapper.unmount()
  })

  it.each([
    ['exit_group_engine_unsupported', '经出口组暂不支持下发'],
    ['udp_engine_unsupported', 'UDP 节点需升级或启用支持，暂未下发'],
    ['advanced_options_engine_unsupported', '高级选项暂不支持下发'],
    ['selection_engine_unsupported', '最小连接数暂不支持下发'],
    ['node_configuration_pending', '配置同步中'],
    ['protocol_verification_pending', '验证中'],
    ['protocol_health_expired', '健康验证过期'],
    ['no_available_nodes', '暂无可用节点'],
  ])('shows %s as a blocking status, not ordinary pending activation', (reason, label) => {
    const rule: ForwardRule = { ...baseRule, activation_reason: reason }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    expect(wrapper.find('.business-status').text()).toBe(label)
    expect(wrapper.find('.business-status').classes()).toContain('business-status--warning')
    expect(wrapper.find('.business-status').text()).not.toBe('待激活')
    wrapper.unmount()
  })
  it('shows the node upgrade requirement for VLESS Reality UDP DIRECT', () => {
    const rule: ForwardRule = { ...baseRule, ingress_protocol: 'vless_reality', vless_outbound_mode: 'DIRECT', protocol: 'udp', status: 'pending_activation', activation_reason: 'udp_engine_unsupported' }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '' } })
    expect(wrapper.find('.business-status').text()).toContain('节点需升级')
    expect(wrapper.find('.business-status').text()).not.toBe('可用')
    wrapper.unmount()
  })
  it('shows the exhausted quota and opens the editor instead of pretending to resume', async () => {
    const rule = { ...baseRule, traffic_limit_bytes: 1000, traffic_used_bytes: 1500, status: 'quota_exhausted' as const }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [rule], devices, ruleGroups: [], selectedIds: [], busyId: '', trafficByRule: { [rule.id]: 1500 } } })
    expect(wrapper.text()).toContain('额度用完，自动暂停')
    expect(wrapper.find('button[title="恢复规则"]').exists()).toBe(false)
    await wrapper.get('.business-desktop button[title="调整流量额度"]').trigger('click')
    expect(wrapper.emitted('edit')?.[0]).toEqual([rule])
    expect(wrapper.emitted('toggle')).toBeUndefined()
    wrapper.unmount()
  })

  it('shows native SOCKS landing on both layouts and never calls unknown routing direct', () => {
    const socks: ForwardRule = { ...baseRule, id: 'socks', ingress_protocol: 'vless_reality', vless_outbound_mode: 'KL_NATIVE', outbound_summary: { mode: 'SOCKS5', host: 'landing.test', port: 1080 } }
    const unknown: ForwardRule = { ...socks, id: 'unknown', outbound_summary: { mode: 'UNKNOWN' } }
    const wrapper = mount(ForwardRuleInventory, { props: { rules: [socks, unknown], devices, networks, ruleGroups: [], selectedIds: [], busyId: '' } })
    const desktop = wrapper.findAll('.rule-exit-cell')
    expect(desktop[0]!.text()).toContain('SOCKS5（SK5）')
    expect(desktop[0]!.text()).toContain('landing.test:1080')
    expect(desktop[1]!.text()).toContain('出站待核实')
    expect(desktop[1]!.text()).not.toContain('组内直出')
    const mobile = wrapper.find('.business-mobile').text()
    expect(mobile).toContain('SOCKS5（SK5）')
    expect(mobile).toContain('landing.test:1080')
    expect(mobile).toContain('出站待核实')
    wrapper.unmount()
  })

})
