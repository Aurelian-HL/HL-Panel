<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { LoaderCircle } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type DirectPolicy, type GroupNetwork, type GroupNetworkInput, type PortRange, type UserGroup } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import GroupChoiceList from '@/components/GroupChoiceList.vue'
import UserGroupChoiceList from '@/components/UserGroupChoiceList.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { validTargetHost } from '@/lib/businessFormatters'
import { displayError } from '@/lib/displayFormatters'
import { networkDirectPolicy } from '@/lib/forwardingRoutes'
import { formatPortRanges, parsePortRanges } from '@/lib/portRanges'

type NetworkForm = Omit<GroupNetworkInput, 'port_ranges' | 'allowed_user_group_ids' | 'allowed_entry_group_ids' | 'fallback_exit_group_id'> & {
  port_ranges: PortRange[]
  allowed_user_group_ids: string[]
  allowed_entry_group_ids: string[]
  fallback_exit_group_id: string
}

const props = defineProps<{ group: DeviceGroup; groups: DeviceGroup[]; userGroups: UserGroup[]; network: GroupNetwork | null }>()
const emit = defineEmits<{ close: []; saved: [network: GroupNetwork] }>()
const isEntry = computed(() => props.group.kind === 'ENTRY')
const initialDirectPolicy = props.network ? networkDirectPolicy(props.network) : 'DISABLED'
const initialRanges = props.network?.port_ranges?.length
  ? props.network.port_ranges
  : isEntry.value
    ? [{ start: props.network?.port_start ?? 10000, end: props.network?.port_end ?? 20000 }]
    : []
const form = reactive<NetworkForm>({
  connect_host: props.network?.connect_host ?? '',
  port_start: props.network?.port_start ?? (isEntry.value ? 10000 : 0),
  port_end: props.network?.port_end ?? (isEntry.value ? 20000 : 0),
  port_ranges: initialRanges.map((item) => ({ ...item })),
  direct_policy: initialDirectPolicy,
  allow_direct: initialDirectPolicy !== 'DISABLED',
  allowed_user_group_ids: [...(props.network?.allowed_user_group_ids ?? [])],
  allowed_entry_group_ids: [...(props.network?.allowed_entry_group_ids ?? [])],
  allowed_exit_group_ids: [...(props.network?.allowed_exit_group_ids ?? [])],
  fallback_exit_group_id: props.network?.fallback_exit_group_id ?? '',
  traffic_multiplier: props.network?.traffic_multiplier ?? 1,
  revision: props.network?.revision ?? 0,
})
const portRangesText = ref(formatPortRanges(form.port_ranges, form.port_start, form.port_end))
const exits = computed(() => props.groups.filter((item) => item.kind === 'EXIT' && item.id !== props.group.id))
const entries = computed(() => props.groups.filter((item) => item.kind === 'ENTRY' && item.id !== props.group.id))
const fallbackExits = computed(() => exits.value.filter((item) => form.allowed_exit_group_ids.includes(item.id)))
const title = computed(() => isEntry.value ? '入口组网络配置' : '落地出口配置')
const directPolicyHelp = computed(() => {
  if (form.direct_policy === 'DISABLED') return '此入口组只能作为前置入口，经出口组后再到目标。'
  if (form.direct_policy === 'FORCED') return '此入口组内的机器直接访问目标，不能再选择出口组。'
  return '创建规则时可在“入口直出”和“经出口组”之间选择。'
})
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
watch(() => form.direct_policy, (policy) => {
  form.allow_direct = policy !== 'DISABLED'
  if (policy === 'FORCED') {
    form.allowed_exit_group_ids = []
    form.fallback_exit_group_id = ''
  }
})
watch(() => [...form.allowed_exit_group_ids], (ids) => {
  if (form.fallback_exit_group_id && !ids.includes(form.fallback_exit_group_id)) form.fallback_exit_group_id = ''
})
async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (isEntry.value && !validTargetHost(form.connect_host.trim())) { error.value = '连接地址需为单个域名或 IP，不包含协议头、端口或路径'; return }
  if (!Number.isFinite(form.traffic_multiplier) || form.traffic_multiplier <= 0) { error.value = '流量倍率必须大于 0'; return }
  let ranges: PortRange[] = []
  if (isEntry.value) {
    try { ranges = parsePortRanges(portRangesText.value) }
    catch (cause) { error.value = displayError(cause); return }
  }
  const directPolicy: DirectPolicy = isEntry.value ? form.direct_policy : 'DISABLED'
  const input: GroupNetworkInput = {
    connect_host: isEntry.value ? form.connect_host.trim() : '',
    port_start: ranges[0]?.start ?? 0,
    port_end: ranges.at(-1)?.end ?? 0,
    port_ranges: ranges,
    direct_policy: directPolicy,
    allow_direct: directPolicy !== 'DISABLED',
    allowed_user_group_ids: [...form.allowed_user_group_ids],
    allowed_entry_group_ids: isEntry.value ? [] : [...form.allowed_entry_group_ids],
    allowed_exit_group_ids: isEntry.value && directPolicy !== 'FORCED' ? [...form.allowed_exit_group_ids] : [],
    fallback_exit_group_id: isEntry.value && directPolicy !== 'FORCED' ? form.fallback_exit_group_id : '',
    traffic_multiplier: form.traffic_multiplier,
    revision: form.revision,
  }
  busy.value = true
  try { emit('saved', await businessApi.saveGroupNetwork(input, props.group.id, mutationKey(input))) }
  catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
</script>
<template>
  <BaseModal :title="title" :description="group.name" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="group-network-editor" class="form-stack ny-compact-form" @submit.prevent="save">
      <template v-if="isEntry">
        <label class="field"><span>客户连接地址</span><input v-model="form.connect_host" required maxlength="253" placeholder="IP 或域名" /></label>
        <label class="field"><span>允许监听端口</span><textarea v-model="portRangesText" rows="2" required placeholder="10000-19999, 21000-21999" /><small class="field-help">支持多段范围或单个端口，用逗号或换行分隔。</small></label>
        <label class="field"><span>入口直出策略</span><select v-model="form.direct_policy"><option value="DISABLED">禁止（只能经出口组）</option><option value="OPTIONAL">可选（按规则选择）</option><option value="FORCED">强制（只能入口直出）</option></select><small class="field-help">{{ directPolicyHelp }}</small></label>
        <GroupChoiceList v-if="form.direct_policy !== 'FORCED'" v-model="form.allowed_exit_group_ids" :groups="exits" label="可用落地出口组" />
        <p v-else class="field-help">强制直出时不配置落地出口组。</p>
        <label v-if="form.direct_policy !== 'FORCED' && form.allowed_exit_group_ids.length" class="field"><span>备用出口组</span><select v-model="form.fallback_exit_group_id"><option value="">不设置</option><option v-for="item in fallbackExits" :key="item.id" :value="item.id">{{ item.name }}</option></select><small class="field-help">主出口不可用时的备用候选；真正切换需数据面健康探测与运行时支持。</small></label>
      </template>
      <GroupChoiceList v-else v-model="form.allowed_entry_group_ids" :groups="entries" label="允许接入的入口组" />
      <UserGroupChoiceList v-model="form.allowed_user_group_ids" :groups="userGroups" label="授权用户组" />
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack">
      <label class="field"><span>流量倍率</span><input v-model.number="form.traffic_multiplier" type="number" min="0.01" max="1000" step="0.01" required /></label>
      </div></details>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="submit" form="group-network-editor" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '保存中' : '确定' }}</button></template>
  </BaseModal>
</template>
