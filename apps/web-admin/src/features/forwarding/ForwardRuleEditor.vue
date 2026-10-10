<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { LoaderCircle } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type ForwardRule, type ForwardRuleInput, type GroupNetwork, type RuleGroup, type IngressProtocol } from '@/api/business'
import { ApiError } from '@/api/http'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { BYTES_PER_GB, parseTargetAddresses, parseVLESSSOCKS5Target, ruleInput, targetAddress } from '@/lib/businessFormatters'
import { displayError } from '@/lib/displayFormatters'
import { networkAllowsDirect, networkAllowsExitGroup } from '@/lib/forwardingRoutes'

const props = defineProps<{ rule: ForwardRule | null; copy: boolean; devices: DeviceGroup[]; networks: GroupNetwork[]; ruleGroups: RuleGroup[] }>()
const emit = defineEmits<{ close: []; saved: [rule: ForwardRule]; unauthorized: [] }>()
const form = reactive<ForwardRuleInput>(props.rule ? { ...ruleInput(props.rule), ...(props.copy ? {
  name: `${props.rule.name} - 副本`, listen_port: 0, revision: 0, paused: true,
  ...(props.rule.ingress_protocol === 'vless_reality' ? { reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '' } : {}),
} : {}) } : {
  name: '', rule_group_id: '', entry_group_id: '', exit_group_id: '', egress_mode: 'DIRECT', ingress_protocol: 'tcp', vless_flow: '', reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '', vless_outbound_mode: 'SOCKS5', vless_socks5_host: '', vless_socks5_port: 0, vless_socks5_username: '', vless_socks5_password: '', protocol: 'tcp',
  listen_port: 0, targets: [{ host: '', port: 443 }], selection_policy: 'round_robin', accept_proxy_protocol: false,
  send_proxy_protocol: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0,
  traffic_limit_bytes: 0, traffic_quota_monthly: false, paused: false, description: '', revision: 0,
})
const trafficLimitGB = ref((form.traffic_limit_bytes ?? 0) / BYTES_PER_GB)
const targetsText = ref(props.rule
  ? props.rule.ingress_protocol === 'vless_reality' && props.rule.vless_socks5_host && props.rule.vless_socks5_port
    ? targetAddress(props.rule.vless_socks5_host, props.rule.vless_socks5_port)
    : props.rule.targets.map((item) => targetAddress(item.host, item.port)).join('\n')
  : '')
const authorizedEntries = computed(() => props.devices.filter((item) => item.kind === 'ENTRY'))
const authorizedExits = computed(() => props.devices.filter((item) => item.kind === 'EXIT'))
const exitAllowsEntry = (exitGroupId: string, entryGroupId: string) => {
  const exitNetwork = props.networks.find((item) => item.group_id === exitGroupId)
  return !exitNetwork?.allowed_entry_group_ids?.length || exitNetwork.allowed_entry_group_ids.includes(entryGroupId)
}
const entries = computed(() => authorizedEntries.value.filter((item) => {
  const configured = props.networks.find((network) => network.group_id === item.id)
  // A device group is the source of truth for membership. Network policy is
  // provisioned from that membership by the control plane, so a freshly
  // created group must be selectable before its projected network record
  // exists. Keep policy filtering only when a record is already present.
  return !configured || networkAllowsDirect(configured) || (!isVlessReality.value && networkAllowsExitGroup(configured) && configured.allowed_exit_group_ids.some((id) => authorizedExits.value.some((exit) => exit.id === id && exitAllowsEntry(exit.id, item.id))))
}))
const network = computed(() => props.networks.find((item) => item.group_id === form.entry_group_id))
const exits = computed(() => authorizedExits.value.filter((item) => network.value?.allowed_exit_group_ids.includes(item.id) && exitAllowsEntry(item.id, form.entry_group_id)))
const directAllowed = computed(() => Boolean(network.value && networkAllowsDirect(network.value)))
const exitAllowed = computed(() => Boolean(network.value && networkAllowsExitGroup(network.value)))
const title = computed(() => props.copy ? '复制规则' : props.rule ? '编辑规则' : '添加规则')
const ingressProtocol = computed<IngressProtocol>(() => form.ingress_protocol ?? (form.protocol === 'udp' ? 'udp' : 'tcp'))
const isVlessReality = computed(() => ingressProtocol.value === 'vless_reality')
const compactVLESSSOCKS5 = computed(() => isVlessReality.value)
const targetPlaceholder = computed(() => compactVLESSSOCKS5.value
  ? '例如 192.217.9.52:35556:用户名:密码'
  : '一行一个 host:port，例如 1.2.3.4:5678、[2001:db8::1]:443')
const runtimeMaterialPending = computed(() => props.rule?.activation_reason === 'vless_runtime_material_pending' && !props.copy)
const socks5RuntimeMaterialPending = computed(() => props.rule?.activation_reason === 'socks5_runtime_material_pending' && !props.copy)
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
watch(() => form.entry_group_id, () => {
  form.exit_group_id = ''
  form.egress_mode = !network.value || networkAllowsDirect(network.value) ? 'DIRECT' : 'EXIT_GROUP'
})
function chooseExit(value: string): void {
  form.exit_group_id = value
  form.egress_mode = value ? 'EXIT_GROUP' : 'DIRECT'
}
watch(() => form.ingress_protocol, (protocol) => {
  const selectedProtocol = protocol ?? (form.protocol === 'udp' ? 'udp' : 'tcp')
  // NY's customer-facing TCP/UDP entry owns the target transport. Keep the
  // two protocol families separate so the form cannot save a contradictory
  // ingress/target combination.
  form.protocol = selectedProtocol === 'udp' ? 'udp' : 'tcp'
  if (selectedProtocol === 'vless_reality') {
    form.egress_mode = 'DIRECT'
    form.exit_group_id = ''
    if (network.value && !networkAllowsDirect(network.value)) form.entry_group_id = ''
    form.vless_flow = 'xtls-rprx-vision'
    form.accept_proxy_protocol = false
    form.send_proxy_protocol = 0
    // The compact target is always an authenticated SOCKS5 upstream.
    form.vless_outbound_mode = 'SOCKS5'
  }
  if (selectedProtocol === 'socks5') {
    // SOCKS5 has its own client handshake but uses the NY TCP transport.
    // Keep the ingress identity explicit and never rewrite it to raw TCP.
    form.accept_proxy_protocol = false
    form.send_proxy_protocol = 0
  }
  if (selectedProtocol !== 'vless_reality') {
    form.vless_flow = ''
    form.reality_server_name = ''
    form.reality_public_key = ''
    form.reality_short_id = ''
    form.reality_destination = ''
    form.vless_outbound_mode = 'DIRECT'
    form.vless_socks5_host = ''
    form.vless_socks5_port = 0
    form.vless_socks5_username = ''
    form.vless_socks5_password = ''
  }
}, { immediate: true })
watch(() => form.vless_outbound_mode, (mode) => {
  if (isVlessReality.value && mode !== 'SOCKS5') {
    form.vless_outbound_mode = 'SOCKS5'
    return
  }
  if (!isVlessReality.value || mode === 'DIRECT') {
    form.vless_socks5_host = ''
    form.vless_socks5_port = 0
    form.vless_socks5_username = ''
    form.vless_socks5_password = ''
  }
  if (mode === 'SOCKS5' && isVlessReality.value) {
    // SOCKS5 receives the final destination from the VLESS request. Do not
    // carry a stale fixed target into the dynamic mode when switching modes.
    targetsText.value = ''
    form.targets = []
  }
})
watch(() => form.protocol, (protocol) => {
  if (form.ingress_protocol && protocol !== form.ingress_protocol) {
    form.protocol = ingressProtocol.value === 'udp' ? 'udp' : 'tcp'
    return
  }
  if (protocol !== 'udp') return
  form.accept_proxy_protocol = false
  form.speed_limit_mbps = 0
  form.ip_limit = 0
  form.connection_limit = 0
  if (form.send_proxy_protocol !== 2) form.send_proxy_protocol = 0
})

async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (!form.name.trim() || !form.entry_group_id) { error.value = '请填写规则名称并选择入口'; return }
  const selectedIngress = ingressProtocol.value
  if (selectedIngress === 'vless_reality' && (form.protocol !== 'tcp' || form.egress_mode !== 'DIRECT')) { error.value = 'VLESS + Reality + Vision 当前仅支持 TCP 入口直出'; return }
  let compactSOCKS5: ReturnType<typeof parseVLESSSOCKS5Target> | null = null
  if (selectedIngress === 'vless_reality') {
    try { compactSOCKS5 = parseVLESSSOCKS5Target(targetsText.value, Boolean(props.rule && !props.copy)) }
    catch (cause) { error.value = displayError(cause); return }
    form.vless_outbound_mode = 'SOCKS5'
    form.vless_socks5_host = compactSOCKS5.host
    form.vless_socks5_port = compactSOCKS5.port
    form.vless_socks5_username = compactSOCKS5.username
    form.vless_socks5_password = compactSOCKS5.password
  }
  // The group member list is enough to create a rule. The control plane
  // creates/updates the projected listener policy; do not force operators to
  // maintain a second endpoint form just to save a forwarding rule.
  if (form.egress_mode === 'DIRECT' && network.value && !directAllowed.value) { error.value = '入口组未开启直出'; return }
  if (form.egress_mode === 'EXIT_GROUP' && network.value && !exitAllowed.value) { error.value = '该入口组不允许经出口组转发'; return }
  if (form.egress_mode === 'EXIT_GROUP' && !form.exit_group_id) { error.value = '请选择已授权的出口组'; return }
  if (form.egress_mode === 'EXIT_GROUP' && !exits.value.some((item) => item.id === form.exit_group_id)) { error.value = '所选出口组已不可用，请重新选择'; return }
  if (!Number.isInteger(form.listen_port) || form.listen_port < 0 || form.listen_port > 65535) { error.value = '监听端口需为 0 至 65535 的整数，0 表示自动分配'; return }
  if (form.listen_port > 0 && network.value) {
    const ranges = network.value.port_ranges?.length ? network.value.port_ranges : [{ start: network.value.port_start, end: network.value.port_end }]
    if (!ranges.some((item) => form.listen_port >= item.start && form.listen_port <= item.end)) { error.value = '监听端口不在入口组允许的端口范围内'; return }
  }
  if (![0, 1, 2, 3].includes(form.send_proxy_protocol ?? 0)) { error.value = '发送 Proxy Protocol 配置无效'; return }
  if (form.protocol === 'udp' && (form.accept_proxy_protocol || form.speed_limit_mbps || form.ip_limit || form.connection_limit || ![0, 2].includes(form.send_proxy_protocol ?? 0))) { error.value = 'UDP 仅支持关闭或 v2 TCP+UDP 发送模式，不支持规则限速、IP/连接数限制和接收 Proxy Protocol'; return }
  for (const [label, value] of [['规则限速', form.speed_limit_mbps ?? 0], ['IP 限制', form.ip_limit ?? 0], ['连接数限制', form.connection_limit ?? 0]] as const) {
    if (!Number.isInteger(value) || value < 0 || value > 1_000_000) { error.value = `${label}需为 0 至 1000000 的整数`; return }
  }
  const dynamicTargets = selectedIngress === 'vless_reality'
  // The compact VLESS value is an authenticated SOCKS5 landing endpoint, not
  // a customer final target. Keep it out of the public target projection.
  try { form.targets = dynamicTargets ? [] : parseTargetAddresses(targetsText.value) } catch (cause) { error.value = displayError(cause); return }
  if (selectedIngress === 'vless_reality' && !dynamicTargets && form.targets.length !== 1) { error.value = '直连 VLESS + Reality + Vision 需要一个最终目标地址'; return }
  if (dynamicTargets && form.targets.length > 1) { error.value = 'SOCKS5 出站最多保留一个兼容目标地址；最终目标由客户端提供'; return }
  // New VLESS rules leave Reality material empty so the control plane can
  // generate it once. Keep already-issued material stable during edits;
  // clearing it would rotate the customer's URI on an unrelated change.
  const input: ForwardRuleInput = { ...form, name: form.name.trim(), description: form.description.trim(), ingress_protocol: selectedIngress, vless_flow: selectedIngress === 'vless_reality' ? 'xtls-rprx-vision' : '', reality_server_name: selectedIngress === 'vless_reality' ? form.reality_server_name : '', reality_public_key: selectedIngress === 'vless_reality' ? form.reality_public_key : '', reality_short_id: selectedIngress === 'vless_reality' ? form.reality_short_id : '', reality_destination: selectedIngress === 'vless_reality' ? form.reality_destination : '', vless_outbound_mode: selectedIngress === 'vless_reality' ? 'SOCKS5' : 'DIRECT', vless_socks5_host: selectedIngress === 'vless_reality' ? form.vless_socks5_host?.trim() : '', vless_socks5_port: selectedIngress === 'vless_reality' ? form.vless_socks5_port : 0, vless_socks5_username: selectedIngress === 'vless_reality' ? form.vless_socks5_username?.trim() : '', vless_socks5_password: selectedIngress === 'vless_reality' ? (form.vless_socks5_password || '') : '', exit_group_id: form.egress_mode === 'DIRECT' ? '' : form.exit_group_id, targets: form.targets.map((item) => ({ host: item.host.trim(), port: item.port })) }
  const quotaBytes = Math.round(Number(trafficLimitGB.value) * BYTES_PER_GB)
  if (!Number.isFinite(Number(trafficLimitGB.value)) || Number(trafficLimitGB.value) < 0 || !Number.isSafeInteger(quotaBytes) || (Number(trafficLimitGB.value) > 0 && quotaBytes < 1)) { error.value = '请输入有效的流量额度'; return }
  input.traffic_limit_bytes = quotaBytes
  busy.value = true
  try { emit('saved', await businessApi.saveRule(input, props.rule && !props.copy ? props.rule.id : null, mutationKey(input))) }
  catch (cause) { if (cause instanceof ApiError && cause.status === 401) emit('unauthorized'); else error.value = displayError(cause) } finally { busy.value = false }
}

</script>

<template>
  <BaseModal :title="title" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="forward-rule-editor" class="form-stack ny-compact-form" @submit.prevent="save">
      <label class="field"><span>名称</span><input v-model="form.name" maxlength="120" required /></label>
      <label class="field"><span>接入协议</span><select v-model="form.ingress_protocol" class="ingress-protocol-selector"><option value="tcp">NY 转发</option><option value="vless_reality">VLESS + Reality + Vision</option><option hidden value="udp">原 NY UDP 规则</option><option hidden value="socks5">原 NY SOCKS5 规则</option></select></label>
      <label class="field"><span>入口</span><select v-model="form.entry_group_id" required><option value="" disabled>请选择入口设备组</option><option v-if="form.entry_group_id && !entries.some(item => item.id === form.entry_group_id)" :value="form.entry_group_id">原入口已不可用</option><option v-for="item in entries" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
      <p v-if="!entries.length" class="field-help">暂无入口设备组，请先添加设备组成员。</p>
      <label class="field"><span>监听端口</span><input :value="form.listen_port || ''" type="number" min="0" max="65535" step="1" placeholder="自动分配" @input="form.listen_port = Number(($event.target as HTMLInputElement).value || 0)" /></label>
      <label class="field"><span>出口</span><select :value="form.exit_group_id" :disabled="!form.entry_group_id || isVlessReality" @change="chooseExit(($event.target as HTMLSelectElement).value)"><option v-if="directAllowed || !form.entry_group_id || isVlessReality || !network" value="">入口组直出</option><option v-else value="" disabled>请选择出口设备组</option><option v-if="form.exit_group_id && !exits.some(item => item.id === form.exit_group_id)" :value="form.exit_group_id">原出口已不可用</option><option v-for="item in exits" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
      <div v-if="form.entry_group_id && form.exit_group_id" class="rule-connection-summary">入口组 → {{ exits.find(item => item.id === form.exit_group_id)?.name || '出口组' }} → 目标地址</div>
      <label class="field"><span>目标地址</span><textarea v-model="targetsText" required rows="4" spellcheck="false" :placeholder="targetPlaceholder" /><small v-if="isVlessReality" class="field-help">新建填写 IP:端口:账号:密码；编辑已有规则可直接保留 IP:端口，服务端沿用已保存的上游凭据。</small></label>
      <label class="field"><span>规则流量额度（GB）</span><input v-model="trafficLimitGB" type="number" min="0" max="8388607" step="any" placeholder="0 为不限量" /><small class="field-help">0 为不限量；1024 MB = 1 GB，1024 GB = 1 TB（按 1024 进制换算）。达到额度后规则自动暂停，提高或取消额度可恢复，手动暂停仍需手动恢复。</small></label>
      <label class="field field--checkbox"><input v-model="form.traffic_quota_monthly" type="checkbox" /><span>每月清零额度</span><small class="field-help">开启后按 UTC 自然月计算：每月 1 日从 0 GB 重新累计，额度上限每月重新可用；关闭后持续累计历史用量。</small></label>
      <small v-if="rule && !copy" class="field-help">已累计 {{ ((rule.traffic_used_bytes ?? 0) / BYTES_PER_GB).toFixed(3) }} GB{{ form.traffic_quota_monthly ? '（按月清零）' : '（持续累计）' }}</small>
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack">
        <label class="field"><span>规则分组</span><select v-model="form.rule_group_id"><option value="">未分组</option><option v-if="form.rule_group_id && !ruleGroups.some(item => item.id === form.rule_group_id)" :value="form.rule_group_id">原分组已不可用</option><option v-for="item in ruleGroups" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label class="field"><span>目标协议</span><select v-model="form.protocol" disabled><option value="tcp">TCP</option><option value="udp">UDP</option></select><small class="field-help">{{ isVlessReality ? 'VLESS + Reality + Vision 解密后固定转为 TCP 目标。' : ingressProtocol === 'udp' ? 'NY UDP 接入固定转发到 UDP 目标。' : ingressProtocol === 'socks5' ? 'NY SOCKS5 客户端入口固定转发到 TCP 目标。' : 'NY TCP 接入固定转发到 TCP 目标。' }}</small></label>
        <label class="field"><span>目标地址选择方式</span><select v-model="form.selection_policy"><option value="round_robin">轮询</option><option value="random">随机</option><option value="ip_hash">IP 哈希</option><option value="least_load">最小连接数</option><option value="failover">故障转移</option></select><small class="field-help">仅用于多个目标地址之间的选择，不影响设备组内机器调度。</small></label>
        <label class="field"><span>接收 Proxy Protocol</span><select v-model="form.accept_proxy_protocol" :disabled="form.protocol === 'udp'"><option :value="false">关闭</option><option :value="true">开启（TCP）</option></select><small class="field-help">开启后，客户端必须发送 Proxy 头，否则连接失败。</small></label>
        <label class="field"><span>发送 Proxy Protocol</span><select v-model.number="form.send_proxy_protocol"><option :value="0">关闭</option><option v-if="form.protocol === 'tcp'" :value="1">v1（TCP）</option><option :value="2">v2（TCP+UDP）</option><option v-if="form.protocol === 'tcp'" :value="3">v2（TCP）</option></select><small class="field-help">目标服务必须支持所选 Proxy Protocol。</small></label>
        <template v-if="form.protocol === 'tcp'">
          <label class="field"><span>规则限速</span><input v-model.number="form.speed_limit_mbps" type="number" min="0" max="1000000" step="1" /><small class="field-help">Mbps；0 表示不限速，并与该入口上的用户限速取更严格值。</small></label>
          <label class="field"><span>IP 限制</span><input v-model.number="form.ip_limit" type="number" min="0" max="1000000" step="1" /><small class="field-help">0 表示不限，并与该入口上的用户限制叠加。</small></label>
          <label class="field"><span>连接数限制</span><input v-model.number="form.connection_limit" type="number" min="0" max="1000000" step="1" /><small class="field-help">0 表示不限，并与该入口上的用户限制叠加。</small></label>
        </template>
        <p v-else class="inline-warning">NY 的规则限速、IP 和连接数限制不适用于 UDP，本规则不会保存这三项。</p>
        <label class="field"><span>状态</span><select v-model="form.paused"><option :value="false">启用</option><option :value="true">暂停</option></select></label>
        <label class="field"><span>备注</span><textarea v-model="form.description" maxlength="500" rows="2" /></label>
      </div></details>
      <p v-if="runtimeMaterialPending" class="inline-warning">等待节点应用配置并通过协议检测。</p>
      <p v-if="socks5RuntimeMaterialPending" class="inline-warning">SOCKS5 节点尚未就绪，规则保存后仍会等待激活。</p>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="submit" form="forward-rule-editor" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '保存中' : '确定' }}</button></template>
  </BaseModal>
</template>
