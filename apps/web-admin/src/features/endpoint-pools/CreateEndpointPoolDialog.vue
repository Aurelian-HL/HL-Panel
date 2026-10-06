<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { LoaderCircle } from '@lucide/vue'

import { api, type CreateEndpointPoolRequest, type DeviceGroup, type EndpointPool, type LoadBalancingStrategy } from '@/api'
import type { ForwardRule, GroupNetwork } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ groups: DeviceGroup[]; networks: GroupNetwork[]; rules: ForwardRule[] }>()
const emit = defineEmits<{ close: []; created: [pool: EndpointPool, replayed: boolean] }>()

const name = ref('')
const ruleId = ref('')
const selectionPolicy = ref<LoadBalancingStrategy>('weighted_least_connections')
const submitting = ref(false)
const errorMessage = ref('')
const mutationKey = useMutationKey()

const policies: Array<{ value: LoadBalancingStrategy; label: string; caption: string }> = [
  { value: 'weighted_least_connections', label: '加权最少连接', caption: '优先交给当前连接较少的健康候选' },
  { value: 'weighted_round_robin', label: '加权轮询', caption: '按候选权重分配新连接' },
  { value: 'rendezvous_hash', label: 'Rendezvous Hash', caption: '按用户键稳定选择健康候选' },
]

function networkFor(rule: ForwardRule): GroupNetwork | undefined {
  const network = props.networks.find((item) => item.group_id === rule.entry_group_id)
  if (!network?.connect_host) return undefined
  const ranges = network.port_ranges?.length ? network.port_ranges : [{ start: network.port_start, end: network.port_end }]
  return ranges.some((range) => rule.listen_port >= range.start && rule.listen_port <= range.end) ? network : undefined
}
const eligibleRules = computed(() => props.rules.filter((rule) =>
  rule.protocol === 'tcp' && (rule.ingress_protocol === 'vless_reality' || rule.ingress_protocol === 'socks5' || rule.ingress_protocol === 'tcp' || !rule.ingress_protocol) &&
  props.groups.some((group) => group.id === rule.entry_group_id && group.kind === 'ENTRY') && networkFor(rule),
))
const selectedRule = computed(() => eligibleRules.value.find((rule) => rule.id === ruleId.value))
const selectedGroup = computed(() => props.groups.find((group) => group.id === selectedRule.value?.entry_group_id))
const selectedNetwork = computed(() => selectedRule.value ? networkFor(selectedRule.value) : undefined)
const canSubmit = computed(() => name.value.trim().length >= 2 && name.value.trim().length <= 128 && Boolean(selectedRule.value && selectedGroup.value && selectedNetwork.value))

watch(ruleId, () => {
  if (!selectedRule.value || !selectedGroup.value) return
  if (!name.value.trim()) name.value = selectedRule.value.name
  selectionPolicy.value = selectedGroup.value.selection_policy
})

async function submit(): Promise<void> {
  if (submitting.value) return
  errorMessage.value = ''
  const normalizedName = name.value.trim()
  if (!canSubmit.value || !selectedRule.value || !selectedNetwork.value) { errorMessage.value = '请选择已配置入口组网络的规则并填写名称'; return }
  submitting.value = true
  const payload = {
    name: normalizedName,
    group_id: selectedRule.value.entry_group_id,
    rule_id: selectedRule.value.id,
    mode: 'SINGLE_SERVICE_ENDPOINT' as const,
    protocol: selectedRule.value.ingress_protocol === 'vless_reality' ? 'vless' as const : selectedRule.value.ingress_protocol === 'socks5' ? 'socks5' as const : 'tcp' as const,
    hostname: selectedNetwork.value.connect_host,
    port: selectedRule.value.listen_port,
    selection_policy: selectionPolicy.value,
  }
  const request: CreateEndpointPoolRequest = { ...payload, idempotency_key: mutationKey(payload) }
  try {
    const result = await api.createEndpointPool(request)
    emit('created', result.pool, result.replayed)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BaseModal title="创建服务端点" width="small" :close-disabled="submitting" @close="$emit('close')">
    <form id="create-endpoint-pool-form" class="form-stack ny-compact-form" @submit.prevent="submit">
      <label class="field"><span>名称</span><input v-model="name" maxlength="128" placeholder="例如：广州统一入口" autofocus /></label>
      <label class="field"><span>转发规则</span><select v-model="ruleId"><option value="" disabled>请选择已配置的入口规则</option><option v-for="rule in eligibleRules" :key="rule.id" :value="rule.id">{{ rule.name }} · {{ rule.listen_port }}</option></select></label>
      <dl v-if="selectedRule && selectedGroup && selectedNetwork" class="endpoint-derived"><div><dt>入口组</dt><dd>{{ selectedGroup.name }}</dd></div><div><dt>协议</dt><dd>{{ selectedRule.ingress_protocol === 'vless_reality' ? 'VLESS + Reality + Vision' : selectedRule.ingress_protocol === 'socks5' ? 'NY SOCKS5' : 'NY TCP' }}</dd></div><div><dt>连接地址</dt><dd>{{ selectedNetwork.connect_host }}:{{ selectedRule.listen_port }}</dd></div></dl>
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack"><label class="field"><span>负载均衡</span><select v-model="selectionPolicy"><option v-for="item in policies" :key="item.value" :value="item.value">{{ item.label }}</option></select></label></div></details>
      <small v-if="selectedGroup" class="field-help">{{ selectedGroup.member_count }} 台成员；配置保存后需完成规则发布和连通验证。</small>
      <p v-if="!eligibleRules.length" class="field-help">暂无可绑定的 NY TCP、NY SOCKS5 或 VLESS 规则。请先配置入口组连接地址和端口范围，再创建对应规则。</p>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </form>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button class="button button--primary" type="submit" form="create-endpoint-pool-form" :disabled="submitting || !canSubmit"><LoaderCircle v-if="submitting" class="spin" :size="14" />{{ submitting ? '保存中' : '确定' }}</button>
    </template>
  </BaseModal>
</template>

<style scoped>
.endpoint-derived { margin: 0; min-width: 0; border-top: 1px solid var(--border-color, #dbe1e7); }
.endpoint-derived > div { display: grid; grid-template-columns: 5.5rem minmax(0, 1fr); gap: .75rem; padding: .6rem 0; border-bottom: 1px solid var(--border-color, #dbe1e7); }
.endpoint-derived dt { color: var(--text-muted, #68737f); }
.endpoint-derived dd { margin: 0; overflow-wrap: anywhere; }
</style>
