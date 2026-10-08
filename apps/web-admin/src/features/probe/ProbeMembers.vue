<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { BadgeCheck, Eye, Settings2 } from '@lucide/vue'

import type { ProbeMember, ProbeUpstreamStatus } from '@/api/probe'
import type { DeviceGroupMember } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { formatDateTime } from '@/lib/displayFormatters'
import { capacityPercent, countryFlag, formatBytes, formatRate, formatUnixTime, formatUptime, hasLiveProbeReading, measuredPercent, monitorStatus, probeReadingState } from './format'
import ProbeMetricBar from './ProbeMetricBar.vue'

const props = withDefaults(defineProps<{ items: ProbeMember[]; groupMembers?: DeviceGroupMember[]; upstreamStatus: ProbeUpstreamStatus; now: number }>(), { groupMembers: () => [] })
const emit = defineEmits<{ editWeight: [member: DeviceGroupMember] }>()
const groupMember = (nodeId: string) => props.groupMembers.find((member) => member.node_id === nodeId && !member.retired_at)
const selectedId = ref<string | null>(null)
const selected = computed(() => props.items.find((item) => item.node_id === selectedId.value) ?? null)
const hoveredId = ref<string | null>(null)
const hovered = computed(() => props.items.find((item) => item.node_id === hoveredId.value) ?? null)
const tooltipPosition = ref({ left: 0, top: 0 })
const rateHoveredId = ref<string | null>(null)
const rateHovered = computed(() => props.items.find((item) => item.node_id === rateHoveredId.value) ?? null)
const rateTooltipPosition = ref({ left: 0, top: 0 })
const nodeName = (member: ProbeMember) => member.name?.trim() || (member.link_status === 'unmanaged' ? `哪吒服务器 #${member.nezha_server_id}` : member.node_id)
const collected = (value?: string) => value?.trim() || '未采集'
const live = (member: ProbeMember) => hasLiveProbeReading(member, props.upstreamStatus, props.now)
const metric = (member: ProbeMember, key: keyof ProbeMember): number | undefined => {
  const value = member[key]
  return live(member) && typeof value === 'number' ? value : undefined
}
const connectionCount = (member: ProbeMember): number | undefined => {
  const tcp = metric(member, 'tcp_conn_count')
  const udp = metric(member, 'udp_conn_count')
  return tcp === undefined || udp === undefined ? undefined : tcp + udp
}
const formatCount = (value?: number) => value === undefined ? '未采集' : value.toLocaleString('zh-CN')
const positionTooltip = (event: Event, width: number, height: number) => {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  return {
    left: rect.right + width + 12 <= window.innerWidth ? rect.right + 8 : Math.max(8, rect.left - width - 8),
    top: Math.max(8, Math.min(rect.top - 4, window.innerHeight - height - 8)),
  }
}
const showStatus = (member: ProbeMember, event: Event) => {
  tooltipPosition.value = positionTooltip(event, 230, 112)
  hoveredId.value = member.node_id
}
const hideStatus = () => { hoveredId.value = null }
const showRate = (member: ProbeMember, event: Event) => {
  rateTooltipPosition.value = positionTooltip(event, 150, 84)
  rateHoveredId.value = member.node_id
}
const hideRate = () => { rateHoveredId.value = null }
onMounted(() => {
  window.addEventListener('scroll', hideStatus, true)
  window.addEventListener('scroll', hideRate, true)
  window.addEventListener('resize', hideStatus)
  window.addEventListener('resize', hideRate)
})
onUnmounted(() => {
  window.removeEventListener('scroll', hideStatus, true)
  window.removeEventListener('scroll', hideRate, true)
  window.removeEventListener('resize', hideStatus)
  window.removeEventListener('resize', hideRate)
})
const uptimeDetails = (member: ProbeMember) => [
  `开机时长：${formatUptime(metric(member, 'uptime_seconds'))}`,
  `启动时间：${formatUnixTime(member.host_boot_time)}`,
  `最近上报：${member.sampled_at ? formatDateTime(member.sampled_at) : '未采集'}`,
  `探针注册：${member.registered_at ? formatDateTime(member.registered_at) : '未采集'}`,
].join('\n')
</script>

<template>
  <div class="probe-inventory">
    <div class="table-wrap probe-table-wrap">
      <table class="data-table probe-table">
        <thead><tr><th>状态</th><th>主机属地</th><th>主机 IP</th><th>速率</th><th>开机时长</th><th>流量</th><th>CPU</th><th>内存</th><th>存储</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="member in items" :key="member.node_id">
            <td><span class="probe-status probe-status--icon" :class="`probe-status--${monitorStatus(member, upstreamStatus, now).tone}`" :aria-label="`状态：${monitorStatus(member, upstreamStatus, now).text}`" :aria-describedby="hoveredId === member.node_id ? 'probe-status-tooltip' : undefined" tabindex="0" @mouseenter="showStatus(member, $event)" @mouseleave="hideStatus" @focus="showStatus(member, $event)" @blur="hideStatus"><BadgeCheck v-if="monitorStatus(member, upstreamStatus, now).text === '在线'" :size="17" /><span :class="{ 'sr-only': monitorStatus(member, upstreamStatus, now).text === '在线' }">{{ monitorStatus(member, upstreamStatus, now).text }}</span></span></td>
            <td><span class="probe-region" :title="`主机 GeoIP：${collected(member.country_code)}`"><img v-if="countryFlag(member.country_code)" class="probe-region__flag" :src="countryFlag(member.country_code)" alt="" />{{ member.country_code?.toUpperCase() || '未采集' }}</span></td>
            <td class="probe-address"><span :title="`IPv4 · ${collected(member.ipv4)}`">{{ collected(member.ipv4) }}</span><small v-if="member.ipv6" :title="`IPv6 · ${member.ipv6}`">v6 {{ member.ipv6 }}</small></td>
            <td class="probe-measure-pair"><div class="probe-rate-hover" tabindex="0" :aria-describedby="rateHoveredId === member.node_id ? 'probe-rate-tooltip' : undefined" @mouseenter="showRate(member, $event)" @mouseleave="hideRate" @focus="showRate(member, $event)" @blur="hideRate"><span>↑ {{ formatRate(metric(member, 'net_out_speed_bytes_per_second')) }}</span><span>↓ {{ formatRate(metric(member, 'net_in_speed_bytes_per_second')) }}</span></div></td>
            <td><span :title="uptimeDetails(member)">{{ formatUptime(metric(member, 'uptime_seconds')) }}</span></td>
            <td class="probe-measure-pair"><span>↑ {{ formatBytes(metric(member, 'net_out_transfer_bytes')) }}</span><span>↓ {{ formatBytes(metric(member, 'net_in_transfer_bytes')) }}</span></td>
            <td><ProbeMetricBar label="CPU" :percent="measuredPercent(metric(member, 'cpu_percent'))" /></td>
            <td><ProbeMetricBar label="内存" :percent="capacityPercent(metric(member, 'memory_used_bytes'), metric(member, 'memory_total_bytes'))" /></td>
            <td><ProbeMetricBar label="存储" :percent="capacityPercent(metric(member, 'disk_used_bytes'), metric(member, 'disk_total_bytes'))" /></td>
            <td><button v-if="groupMember(member.node_id)" type="button" class="button button--quiet probe-detail-button" :aria-label="`更改${nodeName(member)}权重，当前 ${groupMember(member.node_id)?.weight}`" :title="`权重 ${groupMember(member.node_id)?.weight} · 更改权重`" @click="emit('editWeight', groupMember(member.node_id)!)"><Settings2 :size="17" /><span>{{ groupMember(member.node_id)?.weight }}</span></button><button type="button" class="button button--quiet probe-detail-button" :aria-label="`查看${nodeName(member)}监测详情`" :title="`查看${nodeName(member)}监测详情`" @click="selectedId = member.node_id"><Eye :size="17" /></button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <Teleport to="body">
      <div v-if="hovered" id="probe-status-tooltip" class="probe-status-tooltip" role="tooltip" :style="{ left: `${tooltipPosition.left}px`, top: `${tooltipPosition.top}px` }">
        <div><b>编号:</b> {{ hovered.nezha_server_id ?? (hovered.source === 'hl' ? 'HL 探针' : '未关联') }}</div>
        <div><b>OS:</b> {{ [hovered.host_platform, hovered.host_platform_version].filter(Boolean).join('_') || '未采集' }}</div>
        <div><b>架构:</b> {{ collected(hovered.host_architecture) }}</div>
        <div><b>节点端版本:</b> {{ collected(hovered.agent_version) }}</div>
      </div>
      <div v-if="rateHovered" id="probe-rate-tooltip" class="probe-status-tooltip probe-rate-tooltip" role="tooltip" :style="{ left: `${rateTooltipPosition.left}px`, top: `${rateTooltipPosition.top}px` }">
        <div><b>连接数:</b> {{ formatCount(connectionCount(rateHovered)) }}</div>
        <div><b>TCP:</b> {{ formatCount(metric(rateHovered, 'tcp_conn_count')) }}</div>
        <div><b>UDP:</b> {{ formatCount(metric(rateHovered, 'udp_conn_count')) }}</div>
      </div>
    </Teleport>

    <div class="probe-mobile-list">
      <article v-for="member in items" :key="member.node_id" class="probe-mobile-item">
        <header><strong>{{ collected(member.ipv4) }}</strong><span class="probe-status" :class="`probe-status--${monitorStatus(member, upstreamStatus, now).tone}`">{{ monitorStatus(member, upstreamStatus, now).text }}</span></header>
        <div class="probe-mobile-item__body">
          <dl class="probe-mobile-facts"><div><dt>主机 IPv6</dt><dd>{{ collected(member.ipv6) }}</dd></div><div><dt>主机 GeoIP</dt><dd><span class="probe-region"><img v-if="countryFlag(member.country_code)" class="probe-region__flag" :src="countryFlag(member.country_code)" alt="" />{{ member.country_code?.toUpperCase() || '未采集' }}</span></dd></div><div><dt>线路出口 IP</dt><dd>未验证</dd></div><div><dt>开机时长</dt><dd>{{ formatUptime(metric(member, 'uptime_seconds')) }}</dd></div></dl>
          <div class="probe-mobile-network"><div><span>下行</span><strong>{{ formatRate(metric(member, 'net_in_speed_bytes_per_second')) }}</strong><small>累计 {{ formatBytes(metric(member, 'net_in_transfer_bytes')) }}</small></div><div><span>上行</span><strong>{{ formatRate(metric(member, 'net_out_speed_bytes_per_second')) }}</strong><small>累计 {{ formatBytes(metric(member, 'net_out_transfer_bytes')) }}</small></div></div>
          <div class="probe-meter-list"><ProbeMetricBar label="CPU" :percent="measuredPercent(metric(member, 'cpu_percent'))" /><ProbeMetricBar label="内存" :percent="capacityPercent(metric(member, 'memory_used_bytes'), metric(member, 'memory_total_bytes'))" /><ProbeMetricBar label="磁盘" :percent="capacityPercent(metric(member, 'disk_used_bytes'), metric(member, 'disk_total_bytes'))" /></div>
        </div>
        <footer><small :class="{ 'probe-stale': !live(member) }">{{ probeReadingState(member, upstreamStatus, now) }} · {{ member.sampled_at ? formatDateTime(member.sampled_at) : '未采集' }}</small><div><button v-if="groupMember(member.node_id)" type="button" class="button button--quiet probe-detail-button" :aria-label="`更改${nodeName(member)}权重，当前 ${groupMember(member.node_id)?.weight}`" @click="emit('editWeight', groupMember(member.node_id)!)"><Settings2 :size="16" />{{ groupMember(member.node_id)?.weight }}</button><button type="button" class="button button--quiet probe-detail-button" :aria-label="`查看${nodeName(member)}监测详情`" :title="`查看${nodeName(member)}监测详情`" @click="selectedId = member.node_id"><Eye :size="16" /></button></div></footer>
      </article>
    </div>

    <BaseModal v-if="selected" :title="nodeName(selected)" description="HL主机监测详情" width="large" @close="selectedId = null">
      <div class="probe-details">
        <div class="probe-details__heading"><span class="probe-status" :class="`probe-status--${monitorStatus(selected, upstreamStatus, now).tone}`">{{ monitorStatus(selected, upstreamStatus, now).text }}</span><span :class="{ 'probe-stale': !live(selected) }">{{ probeReadingState(selected, upstreamStatus, now) }} · {{ selected.sampled_at ? formatDateTime(selected.sampled_at) : '未采集' }}</span></div>
        <dl class="probe-details__facts">
          <div><dt>节点 ID</dt><dd>{{ selected.link_status === 'unmanaged' ? '未关联 HL 节点' : selected.node_id }}</dd></div>
          <div><dt>主机 IPv4</dt><dd>{{ collected(selected.ipv4) }}</dd></div>
          <div><dt>主机 IPv6</dt><dd>{{ collected(selected.ipv6) }}</dd></div>
          <div><dt>主机 GeoIP 国家</dt><dd>{{ collected(selected.country_code) }}</dd></div>
          <div><dt>系统 / 架构</dt><dd>{{ [selected.host_platform, selected.host_platform_version, selected.host_architecture].filter(Boolean).join(' · ') || '未采集' }}</dd></div>
          <div><dt>探针版本</dt><dd>{{ collected(selected.agent_version) }}</dd></div>
          <div><dt>启动时间</dt><dd>{{ formatUnixTime(selected.host_boot_time) }}</dd></div>
          <div><dt>最近上报</dt><dd>{{ selected.sampled_at ? formatDateTime(selected.sampled_at) : '未采集' }}</dd></div>
          <div><dt>线路出口 IP</dt><dd>未验证</dd></div>
          <div><dt>开机时长</dt><dd>{{ formatUptime(metric(selected, 'uptime_seconds')) }}</dd></div>
          <div><dt>下行速率 / 累计</dt><dd>{{ formatRate(metric(selected, 'net_in_speed_bytes_per_second')) }} / {{ formatBytes(metric(selected, 'net_in_transfer_bytes')) }}</dd></div>
          <div><dt>上行速率 / 累计</dt><dd>{{ formatRate(metric(selected, 'net_out_speed_bytes_per_second')) }} / {{ formatBytes(metric(selected, 'net_out_transfer_bytes')) }}</dd></div>
          <div><dt>连接数 / TCP / UDP</dt><dd>{{ formatCount(connectionCount(selected)) }} / {{ formatCount(metric(selected, 'tcp_conn_count')) }} / {{ formatCount(metric(selected, 'udp_conn_count')) }}</dd></div>
          <div><dt>内存已用 / 总量</dt><dd>{{ formatBytes(metric(selected, 'memory_used_bytes')) }} / {{ formatBytes(metric(selected, 'memory_total_bytes')) }}</dd></div>
          <div><dt>磁盘已用 / 总量</dt><dd>{{ formatBytes(metric(selected, 'disk_used_bytes')) }} / {{ formatBytes(metric(selected, 'disk_total_bytes')) }}</dd></div>
        </dl>
        <div class="probe-meter-list probe-details__meters"><ProbeMetricBar label="CPU" :percent="measuredPercent(metric(selected, 'cpu_percent'))" /><ProbeMetricBar label="内存" :percent="capacityPercent(metric(selected, 'memory_used_bytes'), metric(selected, 'memory_total_bytes'))" /><ProbeMetricBar label="磁盘" :percent="capacityPercent(metric(selected, 'disk_used_bytes'), metric(selected, 'disk_total_bytes'))" /></div>
      </div>
    </BaseModal>
  </div>
</template>
