<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Settings2 } from '@lucide/vue'

import { api, type DeviceGroup, type DeviceGroupMember } from '@/api'
import { probeApi, type ProbeGroupResponse, type ProbeMember } from '@/api/probe'
import StatePanel from '@/components/StatePanel.vue'
import DeleteNodeDialog from '@/features/nodes/DeleteNodeDialog.vue'
import ProbeMembers from '@/features/probe/ProbeMembers.vue'
import ProbeRefreshSettingsDialog from '@/features/probe/ProbeRefreshSettingsDialog.vue'
import MemberWeightDialog from '@/features/groups/MemberWeightDialog.vue'
import { formatRate, hasLiveProbeReading, monitorStatus } from '@/features/probe/format'
import { readProbeRefreshIntervals, saveProbeRefreshIntervals, type ProbeRefreshIntervals } from '@/features/probe/refreshPreferences'
import { displayError } from '@/lib/displayFormatters'

type StatusFilter = 'all' | 'online' | 'offline'
const groups = ref<DeviceGroup[]>([])
const selectedGroupId = ref('')
const response = ref<ProbeGroupResponse | null>(null)
const groupsLoading = ref(true)
const monitoringLoading = ref(false)
const groupsError = ref('')
const monitoringError = ref('')
const statusFilter = ref<StatusFilter>('all')
const now = ref(Date.now())
const refreshIntervals = ref(readProbeRefreshIntervals())
const settingsOpen = ref(false)
const settingsError = ref('')
const groupMembers = ref<DeviceGroupMember[]>([])
const allGroupMembers = ref<Record<string, DeviceGroupMember[]>>({})
const groupMembersError = ref('')
const editingMember = ref<DeviceGroupMember | null>(null)
const deletingNode = ref<ProbeMember | null>(null)

const availableGroups = computed(() => groups.value.filter((group) => !group.hide_in_probe))
const selectedGroup = computed(() => availableGroups.value.find((group) => group.id === selectedGroupId.value))
const members = computed(() => response.value?.items ?? [])
const upstreamStatus = computed(() => monitoringError.value && response.value ? 'unavailable' : response.value?.upstream_status ?? 'disabled')
const hasProbeSource = computed(() => Boolean(response.value && (upstreamStatus.value === 'ok' || members.value.some((member) => member.source === 'hl'))))
const counts = computed(() => ({
  all: members.value.length,
  online: members.value.filter((member) => monitorStatus(member, upstreamStatus.value, now.value).text === '在线').length,
  offline: members.value.filter((member) => monitorStatus(member, upstreamStatus.value, now.value).text === '离线').length,
}))
const filters: Array<{ value: StatusFilter; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'online', label: '在线' },
  { value: 'offline', label: '离线' },
]
const filteredMembers = computed(() => {
  return members.value.filter((member) => {
    const status = monitorStatus(member, upstreamStatus.value, now.value).text
    return statusFilter.value === 'all' || status === (statusFilter.value === 'online' ? '在线' : '离线')
  })
})
const groupedMembers = computed(() => availableGroups.value.map((group) => ({
  group,
  membership: allGroupMembers.value[group.id] ?? [],
  items: filteredMembers.value.filter((item) => (allGroupMembers.value[group.id] ?? []).some((member) => member.node_id === item.node_id)),
})).filter((section) => section.items.length))
const ungroupedMembers = computed(() => {
  const assigned = new Set(Object.values(allGroupMembers.value).flat().map((member) => member.node_id))
  return filteredMembers.value.filter((item) => !assigned.has(item.node_id))
})

function sumOnlineSpeed(items: typeof members.value, field: 'net_in_speed_bytes_per_second' | 'net_out_speed_bytes_per_second'): number | undefined {
  const online = items.filter((member) => hasLiveProbeReading(member, upstreamStatus.value, now.value))
  if (!online.length || online.some((member) => !hasLiveProbeReading(member, upstreamStatus.value, now.value) || member[field] === undefined)) return undefined
  return online.reduce((sum, member) => sum + (member[field] ?? 0), 0)
}
const inboundSpeed = computed(() => formatRate(sumOnlineSpeed(members.value, 'net_in_speed_bytes_per_second')))
const outboundSpeed = computed(() => formatRate(sumOnlineSpeed(members.value, 'net_out_speed_bytes_per_second')))
const sectionSpeed = (items: typeof members.value, field: 'net_in_speed_bytes_per_second' | 'net_out_speed_bytes_per_second') => formatRate(sumOnlineSpeed(items, field))

let requestId = 0
let memberRequestId = 0
async function loadGroupMembers(): Promise<void> {
  const groupId = selectedGroupId.value
  const currentRequest = ++memberRequestId
  groupMembers.value = []
  groupMembersError.value = ''
  try {
    const targetGroups = groupId ? availableGroups.value.filter((group) => group.id === groupId) : availableGroups.value
    const results = await Promise.all(targetGroups.map(async (group) => {
      const result = await api.getDeviceGroupMembers(group.id)
      return [group.id, result.items.filter((member) => member.group_id === group.id && !member.retired_at)] as const
    }))
    if (currentRequest === memberRequestId) {
      allGroupMembers.value = Object.fromEntries(results)
      groupMembers.value = groupId ? allGroupMembers.value[groupId] ?? [] : []
    }
  } catch (error) {
    if (currentRequest === memberRequestId) {
      allGroupMembers.value = {}
      groupMembersError.value = displayError(error)
    }
  }
}
async function loadMonitoring(clearSnapshot = false): Promise<void> {
  const groupId = selectedGroupId.value
  const currentRequest = ++requestId
  if (clearSnapshot) {
    response.value = null
    monitoringError.value = ''
  }
  monitoringLoading.value = true
  try {
    const result = groupId ? await probeApi.getGroup(groupId) : await probeApi.getInventory()
    if (currentRequest === requestId) {
      now.value = Date.now()
      response.value = result
      monitoringError.value = ''
    }
  } catch (error) {
    if (currentRequest === requestId) monitoringError.value = displayError(error)
  } finally {
    if (currentRequest === requestId) monitoringLoading.value = false
  }
}

function selectGroup(): void {
  statusFilter.value = 'all'
  editingMember.value = null
  void loadGroupMembers()
  void loadMonitoring(true)
}

async function nodeDeleted(nodeId: string): Promise<void> {
  deletingNode.value = null
  requestId++
  memberRequestId++
  if (response.value) response.value = { ...response.value, items: response.value.items.filter((item) => item.node_id !== nodeId) }
  if (editingMember.value?.node_id === nodeId) editingMember.value = null
  await loadGroups()
}

function weightSaved(member: DeviceGroupMember): void {
  groupMembers.value = groupMembers.value.map((item) => item.node_id === member.node_id ? member : item)
  allGroupMembers.value = { ...allGroupMembers.value, [member.group_id]: (allGroupMembers.value[member.group_id] ?? []).map((item) => item.node_id === member.node_id ? member : item) }
  editingMember.value = null
}

async function loadGroups(): Promise<void> {
  groupsLoading.value = true
  groupsError.value = ''
  try {
    groups.value = (await api.getDeviceGroups()).items
    if (selectedGroupId.value && !availableGroups.value.some((group) => group.id === selectedGroupId.value)) {
      selectedGroupId.value = ''
      response.value = null
    }
  } catch (error) {
    groupsError.value = displayError(error)
  } finally {
    groupsLoading.value = false
  }
  await loadGroupMembers()
  await loadMonitoring()
}

let refreshTimer: ReturnType<typeof setTimeout> | undefined
let active = false
function scheduleRefresh(): void {
  if (refreshTimer) clearTimeout(refreshTimer)
  if (!active) return
  refreshTimer = setTimeout(async () => {
    refreshTimer = undefined
    now.value = Date.now()
    if (!monitoringLoading.value) await loadMonitoring()
    scheduleRefresh()
  }, document.visibilityState === 'visible' ? refreshIntervals.value.foreground : refreshIntervals.value.background)
}

function onVisibilityChange(): void {
  now.value = Date.now()
  if (document.visibilityState === 'visible' && !monitoringLoading.value) void loadMonitoring()
  scheduleRefresh()
}

function updateIntervals(next: ProbeRefreshIntervals): void {
  refreshIntervals.value = next
  settingsError.value = ''
  try {
    saveProbeRefreshIntervals(next)
  } catch {
    settingsError.value = '刷新间隔已在当前页面生效，但浏览器未能保存设置。'
  }
  settingsOpen.value = false
  scheduleRefresh()
}
onMounted(() => {
  active = true
  document.addEventListener('visibilitychange', onVisibilityChange)
  void loadGroups().finally(scheduleRefresh)
})
onUnmounted(() => {
  active = false
  requestId++
  memberRequestId++
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (refreshTimer) clearTimeout(refreshTimer)
})
</script>

<template>
  <div class="page-stack probe-page">
    <header class="page-heading">
      <div><h2>设备探针</h2><p>HL主机监控</p></div>
      <div class="page-heading__actions probe-page__actions"><div class="segmented-control" role="group" aria-label="主机状态筛选"><button v-for="filter in filters" :key="filter.value" type="button" :class="{ active: statusFilter === filter.value }" @click="statusFilter = filter.value">{{ filter.label }} {{ counts[filter.value] }}</button></div><button class="button button--secondary" type="button" title="刷新间隔设置" aria-label="刷新间隔设置" @click="settingsOpen = true"><Settings2 :size="16" /></button></div>
    </header>

    <StatePanel v-if="groupsLoading && !response" state="loading" title="正在读取设备探针" />
    <template v-else>
      <section class="probe-overview" aria-label="设备组探针概况">
        <label class="probe-group-select"><span>查看范围</span><select v-model="selectedGroupId" @change="selectGroup"><option value="">全部机器</option><option v-for="group in availableGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
        <div class="probe-overview__metrics"><div><span>成员</span><strong>{{ response ? counts.all : '未采集' }}</strong></div><div><span>在线</span><strong>{{ hasProbeSource ? counts.online : '未采集' }}</strong></div><div><span>下行速率</span><strong>{{ inboundSpeed }}</strong></div><div><span>上行速率</span><strong>{{ outboundSpeed }}</strong></div></div>
      </section>
      <p v-if="selectedGroup" class="probe-group-context">{{ selectedGroup.name }} · {{ selectedGroup.kind === 'EXIT' ? '出口组' : '入口组' }} · {{ selectedGroup.member_count }} 位设备组成员</p>
      <p v-if="response?.upstream_status === 'disabled'" class="inline-warning">哪吒连接未配置。HL 主机探针独立工作，注册后的节点会通过心跳上报指标。</p>
      <p v-else-if="response?.upstream_status === 'unavailable'" class="inline-warning">哪吒服务当前不可用；HL 主机探针仍按各节点最近心跳显示，哪吒指标暂不可用。</p>
      <p v-if="groupsError" class="inline-warning">设备组读取失败，分组列表可能不完整：{{ groupsError }}</p>
      <p v-if="groupMembersError" class="inline-warning">设备组成员读取失败，无法设置权重：{{ groupMembersError }}</p>
      <p v-if="settingsError" class="inline-warning">{{ settingsError }}</p>
      <p v-if="monitoringError && response" class="inline-warning">探针刷新失败，仍显示上次采样：{{ monitoringError }}</p>

      <StatePanel v-if="monitoringLoading && !response" state="loading" title="正在读取探针采样" />
      <StatePanel v-else-if="monitoringError && !response" state="error" title="探针数据加载失败" :message="monitoringError" @retry="loadMonitoring" />
      <StatePanel v-else-if="response && !members.length && upstreamStatus === 'unavailable'" state="error" title="哪吒服务当前不可用" message="暂时无法读取机器清单。" @retry="loadMonitoring" />
      <StatePanel v-else-if="response && !members.length && upstreamStatus === 'disabled'" state="empty" title="尚未注册节点" />
      <StatePanel v-else-if="response && !members.length" state="empty" :title="selectedGroupId ? '设备组暂无成员' : '暂无已上报机器'" />
      <StatePanel v-else-if="response && !filteredMembers.length" state="empty" title="没有符合条件的机器" message="调整状态筛选后重试。" />
      <template v-else-if="response">
        <ProbeMembers v-if="selectedGroupId || groupMembersError" :key="selectedGroupId" :items="filteredMembers" :group-members="groupMembers" :upstream-status="upstreamStatus" :now="now" @edit-weight="editingMember = $event" @delete-node="deletingNode = $event" />
        <div v-else class="probe-sections">
          <section v-for="section in groupedMembers" :key="section.group.id" class="probe-section" :aria-label="`${section.group.name}探针`">
            <header class="probe-section__heading"><span class="probe-section__name">{{ section.group.name }}</span><span class="probe-section__rate"><span>↑ {{ sectionSpeed(section.items, 'net_out_speed_bytes_per_second') }}</span><span>↓ {{ sectionSpeed(section.items, 'net_in_speed_bytes_per_second') }}</span></span></header>
            <ProbeMembers :items="section.items" :group-members="section.membership" :upstream-status="upstreamStatus" :now="now" @edit-weight="editingMember = $event" @delete-node="deletingNode = $event" />
          </section>
          <section v-if="ungroupedMembers.length" class="probe-section" aria-label="未入组机器探针">
            <header class="probe-section__heading"><span class="probe-section__name">未入组机器</span><span class="probe-section__rate"><span>↑ {{ sectionSpeed(ungroupedMembers, 'net_out_speed_bytes_per_second') }}</span><span>↓ {{ sectionSpeed(ungroupedMembers, 'net_in_speed_bytes_per_second') }}</span></span></header>
            <ProbeMembers :items="ungroupedMembers" :upstream-status="upstreamStatus" :now="now" @delete-node="deletingNode = $event" />
          </section>
        </div>
      </template>
    </template>
    <DeleteNodeDialog v-if="deletingNode" :node-id="deletingNode.node_id" :label="[deletingNode.name, deletingNode.ipv4 || deletingNode.node_id].filter(Boolean).join(' · ')" @close="deletingNode = null" @deleted="nodeDeleted" />
    <ProbeRefreshSettingsDialog v-if="settingsOpen" :intervals="refreshIntervals" @close="settingsOpen = false" @save="updateIntervals" />
    <MemberWeightDialog v-if="editingMember" :member="editingMember" :label="[members.find((item) => item.node_id === editingMember?.node_id)?.name || editingMember.node_id, members.find((item) => item.node_id === editingMember?.node_id)?.ipv4 || editingMember.dial_host].filter(Boolean).join(' · ')" @close="editingMember = null" @saved="weightSaved" />
  </div>
</template>
