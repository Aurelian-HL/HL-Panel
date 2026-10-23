<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'

import { api, type DeviceGroup, type DeviceGroupMember } from '@/api'
import { probeApi, type ProbeGroupResponse, type ProbeMember } from '@/api/probe'
import StatePanel from '@/components/StatePanel.vue'
import BaseModal from '@/components/BaseModal.vue'
import DeleteNodeDialog from '@/features/nodes/DeleteNodeDialog.vue'
import ProbeMembers from '@/features/probe/ProbeMembers.vue'
import MemberWeightDialog from '@/features/groups/MemberWeightDialog.vue'
import { formatRate, hasLiveProbeReading, monitorStatus } from '@/features/probe/format'
import { displayError } from '@/lib/displayFormatters'
import { serverNow } from '@/lib/serverClock'
import { loadProbeRemarks, probeRemarkMaxLength, probeRemarksStorageKey, saveProbeRemark } from '@/features/probe/remarks'

type StatusFilter = 'all' | 'online' | 'offline'
const groups = ref<DeviceGroup[]>([])
const selectedGroupId = ref('')
const response = ref<ProbeGroupResponse | null>(null)
const groupsLoading = ref(true)
const monitoringLoading = ref(false)
const groupsError = ref('')
const monitoringError = ref('')
const statusFilter = ref<StatusFilter>('all')
const page = ref(1)
const pageSize = ref(20)
const pageSizeOptions = [10, 20, 50, 100, 200, 500]
const now = ref(serverNow())
const groupMembers = ref<DeviceGroupMember[]>([])
const allGroupMembers = ref<Record<string, DeviceGroupMember[]>>({})
const groupMembersError = ref('')
const editingMember = ref<DeviceGroupMember | null>(null)
const deletingNode = ref<ProbeMember | null>(null)
const deletingUnassigned = ref(false)
const remarks = ref(loadProbeRemarks())
const editingRemark = ref<ProbeMember | null>(null)
const remarkDraft = ref('')
const remarkError = ref('')
function editRemark(member: ProbeMember): void {
  remarks.value = loadProbeRemarks()
  editingRemark.value = member
  remarkDraft.value = remarks.value[member.node_id] ?? ''
  remarkError.value = ''
}
function saveRemark(): void {
  if (!editingRemark.value) return
  try {
    remarks.value = saveProbeRemark(editingRemark.value.node_id, remarkDraft.value)
    editingRemark.value = null
  } catch {
    remarkError.value = remarkDraft.value.trim().length > probeRemarkMaxLength
      ? '备注最多 80 个字符。'
      : '未能保存备注，请允许浏览器保存本站数据后重试。'
  }
}
function onRemarksStorage(event: StorageEvent): void {
  if (event.key === probeRemarksStorageKey || event.key === null) remarks.value = loadProbeRemarks()
}
function askDelete(member: ProbeMember, unassigned = false): void {
  deletingNode.value = member
  deletingUnassigned.value = unassigned
}

const availableGroups = computed(() => groups.value.filter((group) => !group.hide_in_probe))
const selectedGroup = computed(() => availableGroups.value.find((group) => group.id === selectedGroupId.value))
const members = computed(() => response.value?.items ?? [])
const upstreamStatus = computed(() => monitoringError.value && response.value ? 'unavailable' : response.value?.upstream_status ?? 'disabled')
const hasProbeSource = computed(() => Boolean(response.value && (upstreamStatus.value === 'ok' || members.value.some((member) => member.source === 'hl'))))
function isMemberOnline(member: ProbeMember): boolean {
  return monitorStatus(member, upstreamStatus.value, now.value).text === '在线'
}
const counts = computed(() => ({
  all: members.value.length,
  online: members.value.filter(isMemberOnline).length,
  offline: members.value.filter((member) => !isMemberOnline(member)).length,
}))
const filters: Array<{ value: StatusFilter; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'online', label: '在线' },
  { value: 'offline', label: '离线' },
]
const filteredMembers = computed(() => {
  return members.value.filter((member) => {
    const online = isMemberOnline(member)
    return statusFilter.value === 'all' || (statusFilter.value === 'online' ? online : !online)
  })
})
const totalPages = computed(() => Math.max(1, Math.ceil(filteredMembers.value.length / pageSize.value)))
const pagedMembers = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filteredMembers.value.slice(start, start + pageSize.value)
})
const pageStart = computed(() => filteredMembers.value.length ? (page.value - 1) * pageSize.value + 1 : 0)
const pageEnd = computed(() => Math.min(page.value * pageSize.value, filteredMembers.value.length))
const groupedMembers = computed(() => availableGroups.value.map((group) => ({
  group,
  membership: allGroupMembers.value[group.id] ?? [],
  items: pagedMembers.value.filter((item) => (allGroupMembers.value[group.id] ?? []).some((member) => member.node_id === item.node_id)),
})).filter((section) => section.items.length))
const ungroupedMembers = computed(() => {
  const assigned = new Set(Object.values(allGroupMembers.value).flat().map((member) => member.node_id))
  return pagedMembers.value.filter((item) => !assigned.has(item.node_id))
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
let groupRequestId = 0
let groupsInFlight = false
let monitoringController: AbortController | undefined
async function loadGroupMembers(): Promise<void> {
  const groupId = selectedGroupId.value
  const currentRequest = ++memberRequestId
  groupMembersError.value = ''
  try {
    const targetGroups = groupId ? availableGroups.value.filter((group) => group.id === groupId) : availableGroups.value
    const results: Array<readonly [string, DeviceGroupMember[]]> = []
    let next = 0
    async function worker(): Promise<void> {
      while (active && currentRequest === memberRequestId && next < targetGroups.length) {
        const group = targetGroups[next++]!
        const result = await api.getDeviceGroupMembers(group.id)
        results.push([group.id, result.items.filter((member) => member.group_id === group.id && !member.retired_at)])
      }
    }
    const settled = await Promise.allSettled(Array.from({ length: Math.min(4, targetGroups.length) }, worker))
    const failure = settled.find(result => result.status === 'rejected')
    if (failure?.status === 'rejected') throw failure.reason
    if (currentRequest === memberRequestId) {
      allGroupMembers.value = Object.fromEntries(results)
      groupMembers.value = groupId ? allGroupMembers.value[groupId] ?? [] : []
    }
  } catch (error) {
    if (currentRequest === memberRequestId) {
      groupMembersError.value = displayError(error)
    }
  }
}
async function loadMonitoring(clearSnapshot = false): Promise<void> {
  if (!active || (monitoringLoading.value && !clearSnapshot)) return
  monitoringController?.abort()
  const controller = new AbortController()
  monitoringController = controller
  const groupId = selectedGroupId.value
  const currentRequest = ++requestId
  if (clearSnapshot) {
    response.value = null
    monitoringError.value = ''
  }
  monitoringLoading.value = true
  try {
    const result = groupId ? await probeApi.getGroup(groupId, controller.signal) : await probeApi.getInventory(controller.signal)
    if (active && currentRequest === requestId) {
      now.value = serverNow()
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
  page.value = 1
  editingMember.value = null
  groupMembers.value = []
  void loadGroupMembers()
  void loadMonitoring(true)
}

watch([statusFilter, selectedGroupId, pageSize], () => { page.value = 1 })
watch(() => filteredMembers.value.length, () => {
  if (page.value > totalPages.value) page.value = totalPages.value
})

async function nodeDeleted(nodeId: string): Promise<void> {
  deletingNode.value = null
  deletingUnassigned.value = false
  requestId++
  memberRequestId++
  monitoringController?.abort()
  monitoringLoading.value = false
  if (response.value) response.value = { ...response.value, items: response.value.items.filter((item) => item.node_id !== nodeId) }
  if (editingMember.value?.node_id === nodeId) editingMember.value = null
  await Promise.all([loadMonitoring(), loadGroups(true)])
}

function weightSaved(member: DeviceGroupMember): void {
  groupMembers.value = groupMembers.value.map((item) => item.node_id === member.node_id ? member : item)
  allGroupMembers.value = { ...allGroupMembers.value, [member.group_id]: (allGroupMembers.value[member.group_id] ?? []).map((item) => item.node_id === member.node_id ? member : item) }
  editingMember.value = null
}

async function loadGroups(quiet = false): Promise<void> {
  if (!active || groupsInFlight) return
  groupsInFlight = true
  const currentRequest = ++groupRequestId
  if (!quiet) groupsLoading.value = true
  groupsError.value = ''
  try {
    const result = await api.getDeviceGroups()
    if (active && currentRequest === groupRequestId) groups.value = result.items
    if (active && selectedGroupId.value && !availableGroups.value.some((group) => group.id === selectedGroupId.value)) {
      selectedGroupId.value = ''
      response.value = null
      monitoringController?.abort()
      requestId++
      monitoringLoading.value = false
    }
  } catch (error) {
    if (active && currentRequest === groupRequestId) groupsError.value = displayError(error)
  } finally {
    if (active && currentRequest === groupRequestId) groupsLoading.value = false
  }
  try {
    if (!active || currentRequest !== groupRequestId) return
    await Promise.all([loadGroupMembers(), ...(!quiet || !response.value ? [loadMonitoring()] : [])])
  } finally { groupsInFlight = false }
}

let refreshTimer: ReturnType<typeof setTimeout> | undefined
let membershipTimer: ReturnType<typeof setTimeout> | undefined
let active = false
function scheduleMembershipRefresh(): void {
  if (membershipTimer) clearTimeout(membershipTimer)
  if (!active || document.visibilityState !== 'visible') return
  membershipTimer = setTimeout(async () => {
    await loadGroups(true)
    scheduleMembershipRefresh()
  }, 15_000)
}
function scheduleRefresh(): void {
  if (refreshTimer) clearTimeout(refreshTimer)
  if (!active) return
  refreshTimer = setTimeout(() => {
    refreshTimer = undefined
    now.value = serverNow()
    void loadMonitoring()
    scheduleRefresh()
  }, document.visibilityState === 'visible' ? 1_000 : 10_000)
}

function onVisibilityChange(): void {
  now.value = serverNow()
  if (document.visibilityState === 'visible') {
    void loadMonitoring()
    void loadGroups(true)
  }
  scheduleRefresh()
  scheduleMembershipRefresh()
}

function onOnline(): void { onVisibilityChange() }

onMounted(() => {
  active = true
  window.addEventListener('storage', onRemarksStorage)
  document.addEventListener('visibilitychange', onVisibilityChange)
  window.addEventListener('online', onOnline)
  void loadMonitoring()
  void loadGroups(true)
  scheduleRefresh()
  scheduleMembershipRefresh()
})
onUnmounted(() => {
  active = false
  window.removeEventListener('storage', onRemarksStorage)
  requestId++
  memberRequestId++
  groupRequestId++
  document.removeEventListener('visibilitychange', onVisibilityChange)
  window.removeEventListener('online', onOnline)
  monitoringController?.abort()
  if (refreshTimer) clearTimeout(refreshTimer)
  if (membershipTimer) clearTimeout(membershipTimer)
})
</script>

<template>
  <div class="page-stack probe-page">
    <header class="page-heading">
      <div><h2>设备探针</h2><p>HL主机监控</p></div>
      <div class="page-heading__actions probe-page__actions"></div>
    </header>

    <StatePanel v-if="groupsLoading && !response" state="loading" title="正在读取设备探针" />
    <template v-else>
      <section class="probe-overview" aria-label="设备组探针概况">
        <label class="probe-group-select"><span>查看范围</span><select v-model="selectedGroupId" @change="selectGroup"><option value="">全部机器</option><option v-for="group in availableGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
        <div class="probe-overview__metrics"><div><span>成员</span><strong>{{ response ? counts.all : '未采集' }}</strong></div><div><span>在线</span><strong>{{ hasProbeSource ? counts.online : '未采集' }}</strong></div><div><span>下行速率</span><strong>{{ inboundSpeed }}</strong></div><div><span>上行速率</span><strong>{{ outboundSpeed }}</strong></div></div>
      </section>
      <p v-if="selectedGroup" class="probe-group-context">{{ selectedGroup.name }} · {{ selectedGroup.kind === 'EXIT' ? '出口组' : '入口组' }} · {{ selectedGroup.member_count }} 位设备组成员</p>
      <p v-if="response?.upstream_status === 'unavailable'" class="inline-warning">哪吒服务当前不可用；HL 主机探针仍按各节点最近心跳显示，哪吒指标暂不可用。</p>
      <p v-if="groupsError" class="inline-warning">设备组读取失败，分组列表可能不完整：{{ groupsError }}</p>
      <p v-if="groupMembersError" class="inline-warning">设备组成员读取失败，无法设置权重：{{ groupMembersError }}</p>
      <p v-if="monitoringError && response" class="inline-warning">探针刷新失败，仍显示上次采样：{{ monitoringError }}</p>

      <StatePanel v-if="monitoringLoading && !response" state="loading" title="正在读取探针采样" />
      <StatePanel v-else-if="monitoringError && !response" state="error" title="探针数据加载失败" :message="monitoringError" @retry="loadMonitoring" />
      <StatePanel v-else-if="response && !members.length && upstreamStatus === 'unavailable'" state="error" title="哪吒服务当前不可用" message="暂时无法读取机器清单。" @retry="loadMonitoring" />
      <StatePanel v-else-if="response && !members.length && upstreamStatus === 'disabled'" state="empty" title="尚未注册节点" />
      <StatePanel v-else-if="response && !members.length" state="empty" :title="selectedGroupId ? '设备组暂无成员' : '暂无已上报机器'" />
      <StatePanel v-else-if="response && !filteredMembers.length" state="empty" title="没有符合条件的机器" message="调整状态筛选后重试。" />
      <template v-else-if="response">
        <ProbeMembers v-if="selectedGroupId || groupMembersError" :key="selectedGroupId" :items="pagedMembers" :remarks="remarks" :group-members="groupMembers" :upstream-status="upstreamStatus" :now="now" @edit-remark="editRemark" @edit-weight="editingMember = $event" @delete-node="askDelete($event)" />
        <div v-else class="probe-sections">
          <section v-for="section in groupedMembers" :key="section.group.id" class="probe-section" :aria-label="`${section.group.name}探针`">
            <header class="probe-section__heading"><span class="probe-section__name">{{ section.group.name }}</span><span class="probe-section__rate"><span>↑ {{ sectionSpeed(section.items, 'net_out_speed_bytes_per_second') }}</span><span>↓ {{ sectionSpeed(section.items, 'net_in_speed_bytes_per_second') }}</span></span></header>
            <ProbeMembers :items="section.items" :remarks="remarks" :group-members="section.membership" :upstream-status="upstreamStatus" :now="now" @edit-remark="editRemark" @edit-weight="editingMember = $event" @delete-node="askDelete($event)" />
          </section>
          <section v-if="ungroupedMembers.length" class="probe-section" aria-label="未入组机器探针">
            <header class="probe-section__heading"><span class="probe-section__name">未入组机器</span><span class="probe-section__rate"><span>↑ {{ sectionSpeed(ungroupedMembers, 'net_out_speed_bytes_per_second') }}</span><span>↓ {{ sectionSpeed(ungroupedMembers, 'net_in_speed_bytes_per_second') }}</span></span></header>
            <ProbeMembers :items="ungroupedMembers" :remarks="remarks" unassigned :upstream-status="upstreamStatus" :now="now" @edit-remark="editRemark" @delete-node="askDelete($event, true)" />
          </section>
        </div>
      </template>
      <nav v-if="response && members.length" class="probe-pagination" aria-label="设备探针分页与状态筛选">
        <div class="segmented-control probe-pagination__filters" role="group" aria-label="主机状态筛选">
          <button v-for="filter in filters" :key="filter.value" type="button" :class="{ active: statusFilter === filter.value }" @click="statusFilter = filter.value">{{ filter.label }} {{ counts[filter.value] }}</button>
        </div>
        <label class="probe-pagination__size"><span>每页</span><select v-model.number="pageSize" aria-label="每页机器数量"><option v-for="size in pageSizeOptions" :key="size" :value="size">{{ size }}</option></select><span>台</span></label>
        <span class="probe-pagination__summary">显示 {{ pageStart }}-{{ pageEnd }}，共 {{ filteredMembers.length }} 台</span>
        <div class="probe-pagination__buttons"><button class="button button--secondary" type="button" :disabled="page <= 1" @click="page--">上一页</button><span>第 {{ page }} / {{ totalPages }} 页</span><button class="button button--secondary" type="button" :disabled="page >= totalPages" @click="page++">下一页</button></div>
      </nav>
    </template>
    <DeleteNodeDialog v-if="deletingNode" :node-id="deletingNode.node_id" :unassigned="deletingUnassigned" :label="[deletingNode.name, deletingNode.ipv4 || deletingNode.node_id].filter(Boolean).join(' · ')" @close="deletingNode = null; deletingUnassigned = false" @deleted="nodeDeleted" />
    <MemberWeightDialog v-if="editingMember" :member="editingMember" :label="[members.find((item) => item.node_id === editingMember?.node_id)?.name || editingMember.node_id, members.find((item) => item.node_id === editingMember?.node_id)?.ipv4 || editingMember.dial_host].filter(Boolean).join(' · ')" @close="editingMember = null" @saved="weightSaved" />
    <BaseModal v-if="editingRemark" title="设备备注" description="仅保存在当前浏览器，填写后替代列表中的 IP 显示。" width="small" @close="editingRemark = null">
      <form class="form-stack" @submit.prevent="saveRemark">
        <div class="probe-remark-host"><span>真实主机地址</span><strong>{{ editingRemark.ipv4 || editingRemark.ipv6 || '未采集' }}</strong></div>
        <label class="field"><span>显示备注 <small>选填，最多 80 个字符</small></span><input v-model="remarkDraft" :maxlength="probeRemarkMaxLength" placeholder="例如：香港主力 · 01" aria-describedby="probe-remark-hint" /></label>
        <p id="probe-remark-hint" class="field-hint">清空并保存即可恢复 IP 显示，不影响节点连接和转发。</p>
        <p v-if="remarkError" class="form-error" role="alert">{{ remarkError }}</p>
        <div class="probe-refresh-dialog__actions"><button class="button button--secondary" type="button" @click="editingRemark = null">取消</button><button class="button button--primary" type="submit">保存备注</button></div>
      </form>
    </BaseModal>
  </div>
</template>
