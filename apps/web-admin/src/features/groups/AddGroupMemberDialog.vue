<script setup lang="ts">
import { computed, ref } from 'vue'
import { LoaderCircle, UserPlus } from '@lucide/vue'

import { api, type DeviceGroup, type EdgeNode } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ group: DeviceGroup; nodes: EdgeNode[] }>()
const emit = defineEmits<{ close: []; added: [assignmentCount: number] }>()
const nodeId = ref('')
const dialHost = ref('')
const weight = ref(100)
const priority = ref(0)
const submitting = ref(false)
const errorMessage = ref('')
const autoDialHost = ref('')
const operationKey = crypto.randomUUID()

const availableNodes = computed(() => props.nodes.filter((node) => node.status !== 'retired'))

function applyNodeDialHost(): void {
  const node = availableNodes.value.find((item) => item.id === nodeId.value)
  if (!node) return
  const current = dialHost.value.trim()
  // Keep a value that was derived from the previous node in sync when the
  // operator switches nodes, while never overwriting a manual override.
  if (!current || (autoDialHost.value && current === autoDialHost.value)) {
    const hostname = node.hostname.trim()
    dialHost.value = hostname
    autoDialHost.value = hostname
  } else {
    autoDialHost.value = ''
  }
}

function onNodeSelect(event: Event): void {
  nodeId.value = (event.target as HTMLSelectElement).value
  applyNodeDialHost()
}

function onDialHostInput(): void {
  if (dialHost.value.trim() !== autoDialHost.value) autoDialHost.value = ''
}

async function submit(): Promise<void> {
  errorMessage.value = ''
  if (!nodeId.value) {
    errorMessage.value = '请选择要加入的节点'
    return
  }
  const normalizedDialHost = dialHost.value.trim()
  if (props.group.kind === 'ENTRY' && !normalizedDialHost) {
    errorMessage.value = '入口组成员需要填写网关可达的 IP 或域名'
    return
  }
  if (normalizedDialHost && (/[/@?#\s]/.test(normalizedDialHost) || normalizedDialHost.includes('://'))) {
    errorMessage.value = '拨号地址只填写 IP 或域名，不含协议、端口和路径'
    return
  }
  if (!Number.isInteger(weight.value) || weight.value < 1 || weight.value > 1000 || !Number.isInteger(priority.value) || priority.value < 0 || priority.value > 1000) {
    errorMessage.value = '权重需要在 1 至 1000 之间，优先级需要在 0 至 1000 之间'
    return
  }
  submitting.value = true
  try {
    const result = await api.addDeviceGroupMember(props.group.id, { node_id: nodeId.value, dial_host: normalizedDialHost, weight: weight.value, priority: priority.value }, operationKey)
    emit('added', result.assignments.length)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BaseModal title="添加设备组成员" :description="`添加到 ${group.name}`" width="small" :close-disabled="submitting" @close="$emit('close')">
    <form id="add-member-form" class="form-stack" @submit.prevent="submit">
      <label class="field">
        <span>节点</span>
        <select v-model="nodeId" autofocus @change="onNodeSelect">
          <option value="" disabled>选择节点</option>
          <option v-for="node in availableNodes" :key="node.id" :value="node.id">{{ node.name || node.hostname }} · {{ node.status }}</option>
        </select>
      </label>
      <label class="field">
        <span>网关拨号地址{{ group.kind === 'ENTRY' ? '（必填）' : '' }}</span>
        <input v-model="dialHost" :required="group.kind === 'ENTRY'" maxlength="253" spellcheck="false" placeholder="默认使用节点主机名，可手动覆盖" @input="onDialHostInput" />
        <small class="field-help">默认使用所选节点的主机名；只有网关无法直接访问时才需要手动改成可达 IP 或域名。添加成员只保存配置，不代表协议已验证或线路已激活。</small>
      </label>
      <div class="field-grid">
        <label class="field"><span>权重</span><input v-model.number="weight" type="number" min="1" max="1000" step="1" /></label>
        <label class="field"><span>优先级</span><input v-model.number="priority" type="number" min="0" max="1000" step="1" /></label>
      </div>
      <p v-if="!availableNodes.length" class="form-note">当前没有可添加的节点，请先注册节点。</p>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </form>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button class="button button--primary" type="submit" form="add-member-form" :disabled="submitting || !availableNodes.length"><LoaderCircle v-if="submitting" class="spin" :size="16" /><UserPlus v-else :size="16" />{{ submitting ? '正在添加' : '添加成员' }}</button>
    </template>
  </BaseModal>
</template>
