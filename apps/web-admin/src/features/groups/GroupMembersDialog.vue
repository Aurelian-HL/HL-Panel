<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { LoaderCircle, Plus, RefreshCw, Settings2, UserMinus } from '@lucide/vue'

import { api, type DeviceGroup, type DeviceGroupMember, type EdgeNode } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { displayError, formatDateTime } from '@/lib/displayFormatters'
import MemberWeightDialog from './MemberWeightDialog.vue'

const props = defineProps<{ group: DeviceGroup; nodes: EdgeNode[] }>()
const emit = defineEmits<{ close: []; add: []; changed: [assignmentCount: number] }>()
const members = ref<DeviceGroupMember[]>([])
const loading = ref(true)
const errorMessage = ref('')
const actionError = ref('')
const confirmingNodeId = ref<string | null>(null)
const submittingNodeId = ref<string | null>(null)
const operationKey = ref<string | null>(null)
const editingMember = ref<DeviceGroupMember | null>(null)

const orderedMembers = computed(() => [...members.value].sort((a, b) => {
  if (Boolean(a.retired_at) !== Boolean(b.retired_at)) return a.retired_at ? 1 : -1
  return a.node_id.localeCompare(b.node_id)
}))
const activeCount = computed(() => members.value.filter((member) => !member.retired_at).length)

function nodeLabel(nodeId: string): string {
  const node = props.nodes.find((item) => item.id === nodeId)
  return node?.name || node?.hostname || nodeId
}

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const response = await api.getDeviceGroupMembers(props.group.id)
    members.value = response.items.filter((item) => item.group_id === props.group.id)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    loading.value = false
  }
}

function askRetire(nodeId: string): void {
  confirmingNodeId.value = nodeId
  operationKey.value = crypto.randomUUID()
  actionError.value = ''
}

async function retire(): Promise<void> {
  const nodeId = confirmingNodeId.value
  if (!nodeId || !operationKey.value || submittingNodeId.value) return
  submittingNodeId.value = nodeId
  actionError.value = ''
  try {
    const result = await api.retireDeviceGroupMember(props.group.id, nodeId, operationKey.value)
    members.value = members.value.map((item) => item.node_id === nodeId ? result.member : item)
    confirmingNodeId.value = null
    operationKey.value = null
    emit('changed', result.assignments.length)
  } catch (error) {
    actionError.value = displayError(error)
  } finally {
    submittingNodeId.value = null
  }
}

function weightSaved(member: DeviceGroupMember): void {
  members.value = members.value.map((item) => item.node_id === member.node_id ? member : item)
  editingMember.value = null
  emit('changed', 0)
}

onMounted(load)
</script>

<template>
  <BaseModal title="设备组成员" :description="`${group.name} · ${activeCount} 台有效机器`" width="large" :close-disabled="!!submittingNodeId" @close="emit('close')">
    <div class="member-toolbar">
      <span>成员与拨号地址</span>
      <button class="button button--secondary" type="button" :disabled="loading || !!submittingNodeId" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" />刷新</button>
    </div>
    <p v-if="loading && !members.length" class="form-note" role="status">正在读取成员</p>
    <div v-else-if="errorMessage && !members.length" class="form-stack"><p class="form-error" role="alert">{{ errorMessage }}</p><button class="button button--secondary" type="button" @click="load">重试</button></div>
    <p v-else-if="!members.length" class="form-note">该设备组还没有成员。</p>
    <template v-else>
      <ul class="member-list">
        <li v-for="member in orderedMembers" :key="member.node_id" class="member-row">
          <div class="member-row__main">
            <strong>{{ nodeLabel(member.node_id) }}</strong>
            <small>{{ member.node_id }}</small>
            <small>{{ member.dial_host || '未设置拨号地址' }} · 权重 {{ member.weight }} · 优先级 {{ member.priority }}</small>
            <small v-if="member.retired_at">退役于 {{ formatDateTime(member.retired_at) }}</small>
          </div>
          <span v-if="member.retired_at" class="member-status">已退役</span>
          <div v-else class="member-row__actions"><button class="button button--quiet" type="button" :disabled="!!submittingNodeId" :aria-label="`更改 ${nodeLabel(member.node_id)} 权重`" @click="editingMember = member"><Settings2 :size="15" />权重</button><button class="button button--quiet" type="button" :disabled="!!submittingNodeId" :aria-label="`退役 ${nodeLabel(member.node_id)}`" @click="askRetire(member.node_id)"><UserMinus :size="15" />退役</button></div>
        </li>
      </ul>
      <p v-if="errorMessage" class="inline-warning" role="alert">刷新失败，当前显示上次读取的成员：{{ errorMessage }}</p>
    </template>
    <div v-if="confirmingNodeId" class="member-confirm">
      <strong>确认退役 {{ nodeLabel(confirmingNodeId) }}？</strong>
      <p>该机器将退出设备组的后续调度；已有连接需重新连接才能切换。</p>
      <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
      <div class="member-confirm__actions">
        <button class="button button--secondary" type="button" :disabled="!!submittingNodeId" @click="confirmingNodeId = null; operationKey = null">取消</button>
        <button class="button button--primary" type="button" :disabled="!!submittingNodeId" @click="retire"><LoaderCircle v-if="submittingNodeId" class="spin" :size="15" /><UserMinus v-else :size="15" />确认退役</button>
      </div>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="!!submittingNodeId" @click="emit('close')">关闭</button>
      <button class="button button--primary" type="button" :disabled="!!submittingNodeId" @click="emit('add')"><Plus :size="16" />添加成员</button>
    </template>
  </BaseModal>
  <MemberWeightDialog v-if="editingMember" :member="editingMember" :label="`${nodeLabel(editingMember.node_id)} · ${editingMember.dial_host || editingMember.node_id}`" @close="editingMember = null" @saved="weightSaved" />
</template>

<style scoped>
.member-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; font-weight: 650; }
.member-list { list-style: none; margin: 0; padding: 0; }
.member-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 0; border-bottom: 1px solid var(--gray-200); }
.member-row:first-child { border-top: 1px solid var(--gray-200); }
.member-row__main { display: grid; gap: 3px; min-width: 0; overflow-wrap: anywhere; }
.member-row__main strong { font-size: 13px; color: var(--navy-800); }
.member-row__main small { font-size: 11px; color: var(--gray-600); }
.member-row__actions { display: flex; flex: none; gap: 4px; }
.member-status { flex: none; font-size: 11px; color: var(--gray-500); }
.member-confirm { margin-top: 16px; padding: 12px; border: 1px solid #e4d2a9; background: var(--amber-100); }
.member-confirm strong { font-size: 13px; }
.member-confirm p { margin: 6px 0 0; font-size: 12px; line-height: 1.5; }
.member-confirm__actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
@media (max-width: 640px) { .member-row { align-items: flex-start; } .member-row .button { flex: none; } }
</style>
