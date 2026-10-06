<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Clipboard, KeyRound, LoaderCircle, RefreshCw, Trash2 } from '@lucide/vue'

import { api, type DeviceGroup, type DeviceGroupMember, type EnrollmentTokenResponse, type PendingEnrollmentToken } from '@/api'
import { probeApi, type ProbeMember, type ProbeUpstreamStatus } from '@/api/probe'
import BaseModal from '@/components/BaseModal.vue'
import NodeInstallInstructions from '@/features/enrollment/NodeInstallInstructions.vue'
import { displayError, formatDateTime } from '@/lib/displayFormatters'
import { toast } from '@/composables/toast'
import { useMutationKey } from '@/composables/useMutationKey'

export type GroupIntegrationMode = 'online' | 'overseas' | 'offline' | 'config'

const props = defineProps<{ group: DeviceGroup; mode?: GroupIntegrationMode }>()
defineEmits<{ close: [] }>()
const mode = ref<GroupIntegrationMode>(props.mode ?? 'online')
const ttl = ref(900)
const busy = ref(false)
const error = ref('')
const token = ref<EnrollmentTokenResponse | null>(null)
const copied = ref(false)
const selectedNezhaId = ref<number | ''>('')
const issuedNezhaId = ref<number | null>(null)
const probeServers = ref<ProbeMember[]>([])
const probeStatus = ref<ProbeUpstreamStatus>('disabled')
const probeLoading = ref(false)
const probeError = ref('')
const members = ref<DeviceGroupMember[]>([])
const membersLoading = ref(false)
const membersError = ref('')
const pendingTokens = ref<PendingEnrollmentToken[]>([])
const tokensLoading = ref(false)
const tokensError = ref('')
const revokingId = ref<string | null>(null)
const confirmRevokeId = ref<string | null>(null)
const revokeKey = useMutationKey()
const groupTokenName = computed(() => `${props.group.name} 节点注册`)
const availableProbeServers = computed(() => probeServers.value.filter((server) => server.link_status === 'unmanaged' && server.nezha_server_id && !pendingTokens.value.some((pending) => pending.nezha_server_id === server.nezha_server_id)))
const selectedProbeServer = computed(() => availableProbeServers.value.find((server) => server.nezha_server_id === selectedNezhaId.value))

async function loadProbeServers(): Promise<void> {
  probeLoading.value = true
  probeError.value = ''
  try {
    const response = await probeApi.getInventory()
    probeServers.value = response.items
    probeStatus.value = response.upstream_status
  } catch (cause) {
    probeError.value = displayError(cause)
    probeStatus.value = 'unavailable'
  } finally {
    probeLoading.value = false
  }
}

async function loadPendingTokens(): Promise<void> {
  tokensLoading.value = true
  tokensError.value = ''
  try {
    const response = await api.getPendingGroupEnrollmentTokens(props.group.id)
    pendingTokens.value = response.items
  } catch (cause) {
    tokensError.value = displayError(cause)
  } finally {
    tokensLoading.value = false
  }
}

async function revoke(tokenId: string): Promise<void> {
  if (revokingId.value || confirmRevokeId.value !== tokenId) return
  revokingId.value = tokenId
  tokensError.value = ''
  try {
    const result = await api.revokeEnrollmentToken(tokenId, revokeKey({ token_id: tokenId }))
    if (result.token.id !== tokenId) throw new Error('服务返回的令牌 ID 与请求不一致')
    pendingTokens.value = pendingTokens.value.filter((item) => item.id !== tokenId)
    if (token.value?.id === tokenId) token.value = null
    confirmRevokeId.value = null
    toast.success('未使用令牌已撤销')
  } catch (cause) {
    tokensError.value = displayError(cause)
  } finally {
    revokingId.value = null
  }
}

async function loadMembers(): Promise<void> {
  membersLoading.value = true
  membersError.value = ''
  try {
    const response = await api.getDeviceGroupMembers(props.group.id)
    members.value = response.items.filter((item) => item.group_id === props.group.id)
  } catch (cause) {
    membersError.value = displayError(cause)
  } finally {
    membersLoading.value = false
  }
}

async function issue(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const server = selectedNezhaId.value === '' ? undefined : selectedProbeServer.value
    if (selectedNezhaId.value !== '' && !server?.nezha_server_id) throw new Error('所选哪吒机器已不可用，请刷新列表后重选')
    const response = await api.createEnrollmentToken({
      name: server ? `${groupTokenName.value} · ${server.name || `哪吒 #${server.nezha_server_id}`}` : groupTokenName.value,
      group_id: props.group.id,
      ...(server?.nezha_server_id ? { nezha_server_id: server.nezha_server_id } : {}),
      expires_in_seconds: ttl.value,
    })
    token.value = response
    issuedNezhaId.value = response.nezha_server_id ?? server?.nezha_server_id ?? null
    await loadPendingTokens()
  } catch (cause) {
    error.value = displayError(cause)
  } finally {
    busy.value = false
  }
}

function issueAnother(): void {
  token.value = null
  selectedNezhaId.value = ''
  issuedNezhaId.value = null
  copied.value = false
  void loadProbeServers()
  void loadPendingTokens()
}

async function copy(): Promise<void> {
  if (!token.value) return
  try {
    await navigator.clipboard.writeText(token.value.token)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1600)
  } catch {
    toast.error('复制失败', '请手动选中令牌复制')
  }
}

onMounted(() => {
  if (mode.value === 'config') void loadMembers()
  else {
    void loadPendingTokens()
    void loadProbeServers()
  }
})
</script>

<template>
  <BaseModal :title="mode === 'config' ? '节点配置（调试用）' : '节点对接'" :description="`${group.name} · ${group.kind === 'ENTRY' ? '入口组' : '出口组'}`" width="large" @close="$emit('close')">
    <div class="integration-dialog">
      <div v-if="mode !== 'config'" class="integration-tabs" role="tablist" aria-label="对接方式">
        <button type="button" :class="{ active: mode === 'online' }" role="tab" @click="mode = 'online'">在线安装</button>
        <button type="button" :class="{ active: mode === 'overseas' }" role="tab" @click="mode = 'overseas'">海外主线路</button>
        <button type="button" :class="{ active: mode === 'offline' }" role="tab" @click="mode = 'offline'">离线部署</button>
      </div>

      <section v-if="mode === 'config'" class="integration-panel">
        <div class="integration-panel__heading"><KeyRound :size="18" /><div><h3>当前设备组节点记录</h3><p>这里展示控制面保存的成员、拨号地址和调度参数，仅供排查；不代表节点已经应用最新配置或线路健康。</p></div></div>
        <div class="config-summary"><span>组修订 <strong>{{ group.current_generation }}</strong></span><span>有效成员 <strong>{{ members.filter((item) => !item.retired_at).length }}</strong></span></div>
        <p v-if="membersLoading" class="form-note" role="status">正在读取节点配置记录</p>
        <p v-else-if="membersError" class="form-error" role="alert">{{ membersError }}</p>
        <p v-else-if="!members.length" class="form-note">当前设备组没有节点成员。</p>
        <ul v-else class="node-config-list">
          <li v-for="member in members" :key="member.node_id" :class="{ retired: member.retired_at }"><div><strong>{{ member.node_id }}</strong><small>{{ member.dial_host || '未设置拨号地址' }}</small></div><span>权重 {{ member.weight }} · 优先级 {{ member.priority }}<br />{{ member.retired_at ? '已退役' : '参与调度' }}</span></li>
        </ul>
        <button class="button button--secondary" type="button" :disabled="membersLoading" @click="loadMembers"><RefreshCw :size="15" :class="{ spin: membersLoading }" />刷新节点记录</button>
      </section>
      <section v-else class="integration-panel">
        <template v-if="!token">
          <div class="integration-panel__heading"><KeyRound :size="18" /><div><h3>为一台机器生成组注册令牌</h3><p>每台机器使用独立的一次性令牌。HL Agent 注册后自动加入本组。</p></div></div>
          <label class="field"><span>有效时间</span><select v-model.number="ttl"><option :value="900">15 分钟</option><option :value="1800">30 分钟</option><option :value="3600">1 小时</option></select></label>
          <label class="field"><span>关联已有哪吒节点</span><select v-model.number="selectedNezhaId" :disabled="probeLoading"><option value="">暂不关联</option><option v-for="server in availableProbeServers" :key="server.nezha_server_id" :value="server.nezha_server_id">{{ server.name || `哪吒 #${server.nezha_server_id}` }} · #{{ server.nezha_server_id }}{{ server.ipv4 ? ` · ${server.ipv4}` : '' }}</option></select></label>
          <p v-if="probeLoading" class="form-note" role="status">正在读取哪吒机器清单</p>
          <p v-else-if="probeError" class="form-error" role="alert">哪吒机器清单读取失败：{{ probeError }}</p>
          <p v-else-if="probeStatus !== 'ok'" class="integration-warning">哪吒监控当前不可用；仍可签发组令牌，但暂不关联监控节点。</p>
          <p v-else-if="!availableProbeServers.length" class="form-note">没有可关联的哪吒机器；已关联或已预留令牌的机器不会重复列出。</p>
          <p v-if="selectedNezhaId !== ''" class="integration-note">选中的哪吒 Agent 已负责主机监控；这枚令牌用于安装和注册独立的 HL 转发 Agent。注册成功后才建立关联。</p>
          <p class="integration-note">生成令牌后会显示一条完整安装命令，包含令牌、HL Agent、主机探针、Xray 和 GOST，无需分开安装。</p>
          <p v-if="mode === 'overseas'" class="integration-note">海外节点使用同一安装流程；注册后再配置入口、出口和线路，安装不会自动开通专线。</p>
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        </template>
        <template v-else>
          <div class="integration-panel__heading"><KeyRound :size="18" /><div><h3>注册令牌已生成</h3><p>{{ token.name }} · {{ formatDateTime(token.expires_at) }} 到期</p></div></div>
          <NodeInstallInstructions :offline="mode === 'offline'" :token="token.token" />
          <div class="secret-value"><code>{{ token.token }}</code><button class="button button--secondary" type="button" @click="copy"><Clipboard :size="15" />{{ copied ? '已复制' : '复制令牌' }}</button></div>
          <p class="integration-warning">关闭后无法再次查看。该令牌已绑定 {{ group.name }}{{ issuedNezhaId ? ` 和哪吒 #${issuedNezhaId}` : '' }}；当前仅为待注册，HL Agent 注册成功后才自动入组{{ issuedNezhaId ? '并关联哪吒监控' : '' }}。</p>
          <button class="button button--secondary" type="button" @click="issueAnother"><KeyRound :size="15" />为下一台机器发令牌</button>
        </template>
      </section>
      <section v-if="mode !== 'config'" class="integration-panel pending-token-panel">
        <div class="integration-panel__heading"><KeyRound :size="18" /><div><h3>未使用的组注册令牌</h3><p>只显示令牌名称和到期时间。撤销只阻止这枚未使用令牌注册，不影响已注册节点。</p></div></div>
        <p v-if="tokensLoading" class="form-note" role="status">正在读取未使用令牌</p>
        <p v-if="tokensError" class="form-error" role="alert">{{ tokensError }}</p>
        <p v-if="!tokensLoading && !tokensError && !pendingTokens.length" class="form-note">当前没有未使用的组注册令牌。</p>
        <ul v-if="pendingTokens.length" class="pending-token-list">
          <li v-for="item in pendingTokens" :key="item.id">
            <div><strong>{{ item.name }}</strong><small>{{ formatDateTime(item.expires_at) }} 到期{{ item.nezha_server_id ? ` · 哪吒 #${item.nezha_server_id} · 待 HL Agent 注册` : '' }}</small></div>
            <div v-if="confirmRevokeId === item.id" class="pending-token-list__actions"><span>确认撤销？</span><button class="button button--secondary" type="button" :disabled="!!revokingId" @click="confirmRevokeId = null">取消</button><button class="button button--danger" type="button" :disabled="!!revokingId" @click="revoke(item.id)"><LoaderCircle v-if="revokingId === item.id" class="spin" :size="14" />确认</button></div>
            <button v-else class="button button--quiet" type="button" :disabled="!!revokingId" :aria-label="`撤销 ${item.name}`" @click="confirmRevokeId = item.id"><Trash2 :size="15" />撤销</button>
          </li>
        </ul>
        <button class="button button--secondary" type="button" :disabled="tokensLoading" @click="loadPendingTokens"><RefreshCw :size="15" :class="{ spin: tokensLoading }" />刷新令牌列表</button>
      </section>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" @click="$emit('close')">{{ mode === 'config' || token ? '关闭' : '取消' }}</button>
      <button v-if="mode !== 'config' && !token" class="button button--primary" type="button" :disabled="busy" @click="issue"><LoaderCircle v-if="busy" class="spin" :size="15" />{{ busy ? '生成中' : '生成注册令牌' }}</button>
    </template>
  </BaseModal>
</template>

<style scoped>
.integration-dialog { display: grid; gap: 16px; }
.integration-tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border-bottom: 1px solid var(--gray-200); }
.integration-tabs button { min-height: 40px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--gray-600); font-size: 13px; cursor: pointer; }
.integration-tabs button.active { border-bottom-color: var(--gold-500); color: var(--navy-900); font-weight: 650; }
.integration-panel { display: grid; gap: 16px; }
.integration-panel__heading { display: flex; gap: 10px; align-items: flex-start; color: var(--navy-800); }
.integration-panel__heading svg { flex: 0 0 auto; margin-top: 2px; color: var(--gold-600); }
.integration-panel__heading h3 { font-size: 14px; }
.integration-panel__heading p { margin-top: 5px; color: var(--gray-600); font-size: 12px; line-height: 1.6; }
.integration-warning, .integration-note { margin: 0; padding: 10px 12px; font-size: 12px; line-height: 1.6; }
.integration-warning { border: 1px solid #e4d2a9; background: var(--amber-100); color: var(--gray-700); }
.integration-note { border: 1px solid var(--gray-200); background: var(--gray-50); color: var(--gray-600); }
.config-summary { display: flex; flex-wrap: wrap; gap: 18px; padding: 10px 12px; border: 1px solid var(--gray-200); background: var(--gray-50); color: var(--gray-600); font-size: 12px; }
.config-summary strong { color: var(--navy-800); }
.node-config-list { display: grid; gap: 0; margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--gray-200); }
.node-config-list li { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 11px 0; border-bottom: 1px solid var(--gray-200); }
.node-config-list li.retired { opacity: .62; }
.node-config-list li div { display: grid; gap: 3px; min-width: 0; }
.node-config-list strong, .node-config-list small { overflow-wrap: anywhere; }
.node-config-list strong { color: var(--navy-800); font-size: 12px; }
.node-config-list small, .node-config-list span { color: var(--gray-600); font-size: 11px; line-height: 1.45; }
.pending-token-panel { padding-top: 16px; border-top: 1px solid var(--gray-200); }
.pending-token-list { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--gray-200); }
.pending-token-list li { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 10px 0; border-bottom: 1px solid var(--gray-200); }
.pending-token-list li > div:first-child { display: grid; gap: 3px; min-width: 0; }
.pending-token-list strong { color: var(--navy-800); font-size: 12px; overflow-wrap: anywhere; }
.pending-token-list small, .pending-token-list__actions span { color: var(--gray-600); font-size: 11px; }
.pending-token-list__actions { display: flex; align-items: center; flex-wrap: wrap; gap: 5px; }
.pending-token-list .button { flex: 0 0 auto; min-height: 30px; padding: 4px 8px; font-size: 11px; }
@media (max-width: 640px) { .pending-token-list li { align-items: stretch; flex-direction: column; } .pending-token-list .button { align-self: flex-start; } }
@media (max-width: 640px) { .integration-tabs button { font-size: 12px; } }
</style>
