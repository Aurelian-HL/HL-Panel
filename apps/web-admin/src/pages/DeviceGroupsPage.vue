<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ExternalLink, FolderTree, Plus, RefreshCw, Search, SlidersHorizontal } from '@lucide/vue'

import { api, type DeviceGroup, type DeviceGroupKind, type EdgeNode } from '@/api'
import { businessApi, type GroupNetwork, type UserGroup } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import { toast } from '@/composables/toast'
import AddGroupMemberDialog from '@/features/groups/AddGroupMemberDialog.vue'
import CreateDeviceGroupDialog from '@/features/groups/CreateDeviceGroupDialog.vue'
import DeviceGroupInventory, { type GroupIntegrationMode } from '@/features/groups/DeviceGroupInventory.vue'
import EditDeviceGroupDialog from '@/features/groups/EditDeviceGroupDialog.vue'
import GroupNetworkDialog from '@/features/groups/GroupNetworkDialog.vue'
import GroupMembersDialog from '@/features/groups/GroupMembersDialog.vue'
import GroupIntegrationDialog from '@/features/groups/GroupIntegrationDialog.vue'
import DeleteDeviceGroupDialog from '@/features/groups/DeleteDeviceGroupDialog.vue'
import { displayError } from '@/lib/displayFormatters'
import { RouterLink } from 'vue-router'

const groups = ref<DeviceGroup[]>([])
const nodes = ref<EdgeNode[]>([])
const loading = ref(true)
const errorMessage = ref('')
const query = ref('')
const kindFilter = ref<'ALL' | DeviceGroupKind>('ALL')
const groupFilter = ref<'ALL' | 'UNGROUPED'>('ALL')
const showCreateDialog = ref(false)
const memberGroup = ref<DeviceGroup | null>(null)
const addMemberGroup = ref<DeviceGroup | null>(null)
const networkGroup = ref<DeviceGroup | null>(null)
const editGroup = ref<DeviceGroup | null>(null)
const integrationGroup = ref<DeviceGroup | null>(null)
const integrationMode = ref<GroupIntegrationMode>('online')
const deleteGroup = ref<DeviceGroup | null>(null)
const networks = ref<GroupNetwork[]>([])
const userGroups = ref<UserGroup[]>([])

const kinds: Array<{ value: 'ALL' | DeviceGroupKind; label: string }> = [
  { value: 'ALL', label: '全部' }, { value: 'ENTRY', label: '入口' }, { value: 'EXIT', label: '出口' },
]

const isUngrouped = (group: DeviceGroup): boolean => !group.user_group_id?.trim()
const ungroupedCount = computed(() => groups.value.filter(isUngrouped).length)

const filteredGroups = computed(() => {
  const keyword = query.value.trim().toLowerCase()
  return groups.value.filter((group) => (groupFilter.value === 'ALL' || isUngrouped(group))
    && (kindFilter.value === 'ALL' || group.kind === kindFilter.value)
    && (!keyword || `${group.name} ${group.description}`.toLowerCase().includes(keyword)))
})

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const [groupResponse, nodeResponse, networkResponse, userGroupResponse] = await Promise.all([api.getDeviceGroups(), api.getNodes(), businessApi.groupNetworks(), businessApi.userGroups()])
    groups.value = groupResponse.items
    nodes.value = nodeResponse.items
    networks.value = networkResponse.items
    userGroups.value = userGroupResponse.items
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    loading.value = false
  }
}

function onCreated(group: DeviceGroup): void {
  groups.value.unshift(group)
  showCreateDialog.value = false
  toast.success('设备组已创建', group.name)
}

async function onMemberAdded(assignmentCount: number): Promise<void> {
  memberGroup.value = addMemberGroup.value
  addMemberGroup.value = null
  toast.success('成员已添加', assignmentCount ? `已生成 ${assignmentCount} 个节点配置分配` : '设备组当前没有可编译的组修订')
  await load()
}

async function onMemberRetired(assignmentCount: number): Promise<void> {
  toast.success('成员已退役', assignmentCount ? `已生成 ${assignmentCount} 个节点配置分配` : '该成员不再参与后续调度')
  await load()
}

async function onNetworkSaved(): Promise<void> {
  const savedKind = networkGroup.value?.kind
  networkGroup.value = null
  toast.success('设备组网络配置已保存', savedKind === 'ENTRY' ? '规则可使用该入口地址、端口范围和出站策略' : '出口组已可作为规则的落地出口')
  await load()
}

async function onGroupSaved(group: DeviceGroup): Promise<void> {
  editGroup.value = null
  toast.success('设备组已更新', group.name)
  await load()
}

async function onGroupDeleted(replayed: boolean): Promise<void> {
  const deleted = deleteGroup.value
  deleteGroup.value = null
  if (deleted) groups.value = groups.value.filter((item) => item.id !== deleted.id)
  toast.success('设备组已删除', replayed ? '已重放之前的删除操作' : deleted?.name || '')
  await load()
}

function openIntegration(group: DeviceGroup, mode: GroupIntegrationMode): void {
  integrationGroup.value = group
  integrationMode.value = mode
}

function editNetworkFromGroup(): void {
  networkGroup.value = editGroup.value
  editGroup.value = null
}

onMounted(load)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading">
      <div><h2>设备组管理（站点管理员）</h2><p>管理入口与出口设备组、节点成员和路由网络配置</p></div>
      <div class="page-heading__actions">
        <button class="button button--primary" type="button" @click="showCreateDialog = true"><Plus :size="16" />添加设备组</button>
        <button class="button button--secondary" type="button" disabled title="清空流量暂未开放"><SlidersHorizontal :size="16" />清空流量</button>
        <button class="button button--secondary" type="button" :disabled="loading" @click="load"><RefreshCw :class="{ spin: loading }" :size="16" />刷新</button>
        <RouterLink class="button button--secondary" to="/user-groups"><FolderTree :size="16" />管理分组<ExternalLink :size="14" /></RouterLink>
      </div>
    </header>

    <section class="toolbar" aria-label="设备组筛选">
      <div class="segmented-control group-scope-tabs" role="tablist" aria-label="设备组分组筛选">
        <button type="button" role="tab" :aria-selected="groupFilter === 'ALL'" :class="{ active: groupFilter === 'ALL' }" @click="groupFilter = 'ALL'">全部</button>
        <button type="button" role="tab" :aria-selected="groupFilter === 'UNGROUPED'" :class="{ active: groupFilter === 'UNGROUPED' }" @click="groupFilter = 'UNGROUPED'">未分组 <span>({{ ungroupedCount }})</span></button>
      </div>
      <label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索设备组名称或描述" /></label>
      <div class="segmented-control" role="group" aria-label="类型筛选"><button v-for="kind in kinds" :key="kind.value" type="button" :class="{ active: kindFilter === kind.value }" @click="kindFilter = kind.value">{{ kind.label }}</button></div>
    </section>

    <StatePanel v-if="loading && !groups.length" state="loading" title="正在读取设备组" />
    <StatePanel v-else-if="errorMessage && !groups.length" state="error" title="设备组加载失败" :message="errorMessage" @retry="load" />
    <StatePanel v-else-if="!groups.length" state="empty" title="还没有设备组" message="创建设备组，添加机器成员，并配置连接地址与端口范围。" />
    <StatePanel v-else-if="!filteredGroups.length" state="empty" title="没有符合条件的设备组" message="调整搜索词或类型筛选后重试。" />
    <DeviceGroupInventory v-else :groups="filteredGroups" :networks="networks" @add-member="memberGroup = $event" @configure-network="networkGroup = $event" @edit="editGroup = $event" @integrate="openIntegration" @remove="deleteGroup = $event" />
    <p v-if="errorMessage && groups.length" class="inline-warning">刷新失败，当前显示上一次成功读取的数据：{{ errorMessage }}</p>

    <CreateDeviceGroupDialog v-if="showCreateDialog" :user-groups="userGroups" @close="showCreateDialog = false" @created="onCreated" />
    <GroupMembersDialog v-if="memberGroup" :group="memberGroup" :nodes="nodes" @close="memberGroup = null" @add="addMemberGroup = memberGroup; memberGroup = null" @changed="onMemberRetired" />
    <AddGroupMemberDialog v-if="addMemberGroup" :group="addMemberGroup" :nodes="nodes" @close="memberGroup = addMemberGroup; addMemberGroup = null" @added="onMemberAdded" />
    <GroupNetworkDialog v-if="networkGroup" :group="networkGroup" :groups="groups" :user-groups="userGroups" :network="networks.find(item => item.group_id === networkGroup?.id) ?? null" @close="networkGroup = null" @saved="onNetworkSaved" />
    <EditDeviceGroupDialog v-if="editGroup" :group="editGroup" :user-groups="userGroups" :network="networks.find(item => item.group_id === editGroup?.id) ?? null" @close="editGroup = null" @saved="onGroupSaved" @configure-network="editNetworkFromGroup" />
    <GroupIntegrationDialog v-if="integrationGroup" :group="integrationGroup" :mode="integrationMode" @close="integrationGroup = null" />
    <DeleteDeviceGroupDialog v-if="deleteGroup" :group="deleteGroup" @close="deleteGroup = null" @deleted="onGroupDeleted" />
  </div>
</template>
