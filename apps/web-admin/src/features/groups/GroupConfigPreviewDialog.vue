<script setup lang="ts">
import type { DeviceGroup } from '@/api'
import type { GroupNetwork, UserGroup } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { formatPortRanges } from '@/lib/portRanges'
import { networkDirectPolicy } from '@/lib/forwardingRoutes'

const props = defineProps<{
  group: DeviceGroup
  groups: DeviceGroup[]
  network: GroupNetwork | null
  userGroups: UserGroup[]
}>()
defineEmits<{ close: [] }>()

const directPolicy = (network: GroupNetwork | null) => network ? networkDirectPolicy(network) : null
const policyLabel = (value: ReturnType<typeof directPolicy>) => value === 'FORCED' ? '仅入口直出' : value === 'DISABLED' ? '仅经出口组' : value === 'OPTIONAL' ? '入口直出 / 经出口组' : '未配置'
const userGroupNames = (ids: string[] | undefined) => !ids?.length ? '未限制' : ids.map((id) => props.userGroups.find((item) => item.id === id)?.name || id).join('、')
const deviceGroupNames = (ids: string[] | undefined) => !ids?.length ? '未限制' : ids.map((id) => props.groups.find((item) => item.id === id)?.name || id).join('、')
const userGroupName = (id: string | undefined) => !id ? '未指定' : props.userGroups.find((item) => item.id === id)?.name || id
</script>

<template>
  <BaseModal title="设备组配置（控制面）" :description="`${group.name} · 只读诊断视图`" width="large" @close="$emit('close')">
    <div class="group-config-preview">
      <section>
        <h3>设备组</h3>
        <dl class="node-details__grid">
          <div><dt>名称</dt><dd>{{ group.name }}</dd></div>
          <div><dt>类型</dt><dd>{{ group.kind === 'ENTRY' ? '入口组' : group.kind === 'EXIT' ? '出口组' : '历史类型' }}</dd></div>
          <div><dt>成员数量</dt><dd>{{ group.member_count }} 台</dd></div>
          <div><dt>调度策略</dt><dd>{{ group.selection_policy === 'weighted_round_robin' ? '加权轮询' : group.selection_policy === 'weighted_least_connections' ? '加权最少连接' : '稳定哈希' }}</dd></div>
          <div><dt>当前修订</dt><dd>{{ group.current_generation }}</dd></div>
          <div><dt>所属用户组</dt><dd>{{ userGroupName(group.user_group_id) }}</dd></div>
          <div><dt>探针展示</dt><dd>{{ group.hide_in_probe ? '隐藏' : '显示' }}</dd></div>
        </dl>
      </section>

      <section>
        <h3>网络与路径策略</h3>
        <p v-if="!network" class="node-details__empty">该设备组尚未保存网络配置。</p>
        <dl v-else class="node-details__grid">
          <div><dt>{{ group.kind === 'ENTRY' ? '连接地址' : '落地出站' }}</dt><dd>{{ network.connect_host || '由成员拨号地址决定' }}</dd></div>
          <div><dt>端口范围</dt><dd>{{ group.kind === 'ENTRY' ? formatPortRanges(network.port_ranges, network.port_start, network.port_end) : '不适用' }}</dd></div>
          <div><dt>直出策略</dt><dd>{{ group.kind === 'ENTRY' ? policyLabel(directPolicy(network)) : '出口组不执行直出' }}</dd></div>
          <div><dt>允许入口组</dt><dd>{{ deviceGroupNames(network.allowed_entry_group_ids) }}</dd></div>
          <div><dt>允许出口组</dt><dd>{{ deviceGroupNames(network.allowed_exit_group_ids) }}</dd></div>
          <div><dt>备用出口组</dt><dd>{{ network.fallback_exit_group_id ? deviceGroupNames([network.fallback_exit_group_id]) : '未配置' }}</dd></div>
          <div><dt>流量倍率</dt><dd>{{ network.traffic_multiplier }}x</dd></div>
          <div><dt>授权用户组</dt><dd>{{ userGroupNames(network.allowed_user_group_ids) }}</dd></div>
        </dl>
      </section>

      <p class="group-config-preview__notice">这里展示的是控制面保存的组策略，不代表节点已经应用，也不代表真实转发链路已连通。设备主机指标可在“设备探针”查看；配置应用和转发结果仍需通过实际规则验收。</p>
    </div>
    <template #footer><button class="button button--secondary" type="button" @click="$emit('close')">关闭</button></template>
  </BaseModal>
</template>

<style scoped>
.group-config-preview { display: grid; gap: 20px; }
.group-config-preview section + section { padding-top: 18px; border-top: 1px solid var(--gray-100); }
.group-config-preview h3 { margin-bottom: 12px; color: var(--navy-900); font-size: 14px; }
.group-config-preview__notice { margin: 0; padding: 10px 12px; border: 1px solid #e4d2a9; background: var(--amber-100); color: var(--gray-700); font-size: 12px; line-height: 1.6; }
</style>
