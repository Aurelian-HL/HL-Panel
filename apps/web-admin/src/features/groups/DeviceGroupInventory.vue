<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, Link, Network, Pencil, ServerCog, Trash2, UserPlus, UsersRound } from '@lucide/vue'

import type { DeviceGroup, LoadBalancingStrategy } from '@/api'
import type { GroupNetwork } from '@/api/business'
import { formatDateTime } from '@/lib/displayFormatters'
import { networkDirectPolicy } from '@/lib/forwardingRoutes'
import { formatPortRanges } from '@/lib/portRanges'

export type GroupIntegrationMode = 'online' | 'overseas' | 'offline' | 'config'

const props = defineProps<{ groups: DeviceGroup[]; networks: GroupNetwork[]; disabled?: boolean }>()
const emit = defineEmits<{ addMember: [group: DeviceGroup]; configureNetwork: [group: DeviceGroup]; edit: [group: DeviceGroup]; integrate: [group: DeviceGroup, mode: GroupIntegrationMode]; remove: [group: DeviceGroup] }>()
const openMenuGroup = ref<DeviceGroup | null>(null)
const menuElement = ref<HTMLElement | null>(null)
const menuStyle = ref<Record<string, string>>({ visibility: 'hidden' })

async function toggleIntegrationMenu(group: DeviceGroup, event: MouseEvent): Promise<void> {
  if (props.disabled) return
  if (openMenuGroup.value?.id === group.id) {
    closeIntegrationMenu()
    return
  }
  const anchor = event.currentTarget as HTMLElement
  openMenuGroup.value = group
  menuStyle.value = { visibility: 'hidden' }
  await nextTick()
  if (openMenuGroup.value?.id !== group.id) return
  const rect = anchor.getBoundingClientRect()
  const width = Math.min(270, window.innerWidth - 24)
  const height = menuElement.value?.offsetHeight || 160
  const top = window.innerHeight - rect.bottom >= height + 6
    ? rect.bottom + 6
    : Math.max(12, rect.top - height - 6)
  const left = Math.min(Math.max(12, rect.right - width), window.innerWidth - width - 12)
  menuStyle.value = { top: `${top}px`, left: `${left}px`, width: `${width}px` }
}

function selectIntegration(group: DeviceGroup, mode: GroupIntegrationMode): void {
  closeIntegrationMenu()
  if (props.disabled) return
  emit('integrate', group, mode)
}

function closeIntegrationMenu(): void {
  openMenuGroup.value = null
}

function closeOnScroll(event: Event): void {
  if (!menuElement.value?.contains(event.target as Node)) closeIntegrationMenu()
}

onMounted(() => {
  document.addEventListener('click', closeIntegrationMenu)
  document.addEventListener('scroll', closeOnScroll, true)
  window.addEventListener('resize', closeIntegrationMenu)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', closeIntegrationMenu)
  document.removeEventListener('scroll', closeOnScroll, true)
  window.removeEventListener('resize', closeIntegrationMenu)
})
const networkFor = (id: string) => props.networks.find((item) => item.group_id === id)
const isManagedKind = (group: DeviceGroup) => group.kind === 'ENTRY' || group.kind === 'EXIT'
const networkLabel = (group: DeviceGroup) => group.kind === 'EXIT' && networkFor(group.id) ? '落地出站' : networkFor(group.id)?.connect_host || '尚未配置'
const portRangeLabel = (group: DeviceGroup) => {
  const network = networkFor(group.id)
  return network ? formatPortRanges(network.port_ranges, network.port_start, network.port_end) : '尚未配置'
}
const routePolicyLabel = (group: DeviceGroup) => {
  const network = networkFor(group.id)
  if (group.kind !== 'ENTRY' || !network) return '不适用'
  const policy = networkDirectPolicy(network)
  if (policy === 'DISABLED') return '仅经出口组'
  if (policy === 'FORCED') return '仅入口直出'
  return '入口直出 / 经出口组'
}

const kindLabels = { ENTRY: '入口', EXIT: '出口', EDGE: '历史类型', HYBRID: '历史类型' }
const strategyLabels: Record<LoadBalancingStrategy, string> = {
  weighted_round_robin: '加权轮询',
  weighted_least_connections: '加权最少连接',
  rendezvous_hash: '稳定哈希',
}
</script>

<template>
  <fieldset class="inventory group-inventory" :disabled="disabled" aria-label="设备组列表">
    <div class="table-wrap group-table-wrap">
      <table class="data-table group-table">
        <thead><tr><th>设备组</th><th>类型 / 成员</th><th>网络配置</th><th>最近更新</th><th class="align-right">操作</th></tr></thead>
        <tbody>
          <tr v-for="group in groups" :key="group.id">
            <td><div class="resource-name"><span><ServerCog :size="17" /></span><div><strong>{{ group.name }}</strong><small>{{ group.description || '未填写描述' }}</small></div></div></td>
            <td><span class="kind-badge">{{ kindLabels[group.kind] }}</span><small class="table-secondary">{{ group.member_count }} 台机器</small></td>
            <td><strong class="table-primary">{{ networkLabel(group) }}</strong><small v-if="networkFor(group.id) && group.kind === 'ENTRY'" class="table-secondary">{{ portRangeLabel(group) }} · {{ routePolicyLabel(group) }}</small></td>
            <td><strong class="table-primary">{{ formatDateTime(group.updated_at) }}</strong></td>
            <td><template v-if="isManagedKind(group)"><div class="business-row-actions"><div class="integration-menu"><button class="button button--secondary integration-menu__toggle" type="button" aria-haspopup="menu" :aria-expanded="openMenuGroup?.id === group.id" @click.stop="toggleIntegrationMenu(group, $event)"><Link :size="15" />对接<ChevronDown :size="14" /></button></div><button class="button button--quiet" type="button" @click="emit('configureNetwork', group)"><Network :size="15" />{{ group.kind === 'ENTRY' ? '连接' : '落地' }}</button><button class="button button--quiet" type="button" aria-label="编辑设备组" title="编辑设备组" @click="emit('edit', group)"><Pencil :size="15" />编辑</button><button class="button button--quiet" type="button" @click="emit('addMember', group)"><UserPlus :size="15" />成员</button><button class="button button--quiet button--danger" type="button" aria-label="删除设备组" title="删除设备组" @click="emit('remove', group)"><Trash2 :size="15" />删除</button></div></template><span v-else class="table-secondary">历史类型，仅查看</span></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="mobile-resource-list">
      <article v-for="group in groups" :key="group.id" class="mobile-resource-card group-mobile-card">
        <header><div class="resource-name"><span><ServerCog :size="17" /></span><div><strong>{{ group.name }}</strong><small>{{ group.description || '未填写描述' }}</small></div></div><span class="kind-badge">{{ kindLabels[group.kind] }}</span></header>
        <dl><div><dt><UsersRound :size="13" />机器成员</dt><dd>{{ group.member_count }}</dd></div><div><dt>{{ group.kind === 'ENTRY' ? '连接地址' : '落地能力' }}</dt><dd>{{ networkLabel(group) }}</dd></div><div><dt>端口范围</dt><dd>{{ group.kind === 'EXIT' ? '不适用' : portRangeLabel(group) }}</dd></div><div><dt>出站策略</dt><dd>{{ routePolicyLabel(group) }}</dd></div><div><dt>分配方式</dt><dd>{{ strategyLabels[group.selection_policy] }}</dd></div></dl>
        <template v-if="isManagedKind(group)"><footer><div class="integration-menu"><button class="button button--secondary integration-menu__toggle" type="button" aria-haspopup="menu" :aria-expanded="openMenuGroup?.id === group.id" @click.stop="toggleIntegrationMenu(group, $event)"><Link :size="15" />对接<ChevronDown :size="14" /></button></div><button class="button button--quiet" type="button" @click="emit('edit', group)"><Pencil :size="15" />编辑</button><button class="button button--quiet" type="button" @click="emit('configureNetwork', group)"><Network :size="15" />{{ group.kind === 'ENTRY' ? '连接' : '落地' }}</button><button class="button button--quiet" type="button" @click="emit('addMember', group)"><UserPlus :size="15" />成员</button><button class="button button--quiet button--danger" type="button" aria-label="删除设备组" title="删除设备组" @click="emit('remove', group)"><Trash2 :size="15" />删除</button></footer></template><p v-else class="table-secondary">历史类型，仅查看</p>
      </article>
    </div>
    <Teleport to="body">
      <div v-if="openMenuGroup" ref="menuElement" class="integration-menu__items" :style="menuStyle" role="menu" @keydown.esc="closeIntegrationMenu">
        <button type="button" role="menuitem" @click="selectIntegration(openMenuGroup, 'online')">自动探测线路（安装器待发布）</button>
        <button type="button" role="menuitem" @click="selectIntegration(openMenuGroup, 'overseas')">海外节点安装</button>
        <button type="button" role="menuitem" @click="selectIntegration(openMenuGroup, 'offline')">离线部署</button>
        <button type="button" role="menuitem" @click="selectIntegration(openMenuGroup, 'config')">查看节点记录（调试用）</button>
      </div>
    </Teleport>
  </fieldset>
</template>

<style scoped>
.group-inventory { min-width: 0; margin: 0; padding: 0; border: 0; }
.group-inventory { min-width: 0; }
.group-table-wrap { display: block; }
.group-table { width: 100%; min-width: 0; table-layout: fixed; }
.group-table th:first-child { width: 22%; }
.group-table th:nth-child(2) { width: 12%; }
.group-table th:nth-child(3) { width: 24%; }
.group-table th:nth-child(4) { width: 16%; }
.group-table th:last-child { width: 26%; }
.group-table .business-row-actions { flex-wrap: nowrap; justify-content: flex-end; gap: 2px; white-space: nowrap; }
.group-table .business-row-actions .button { flex: 0 0 auto; padding-right: 6px; padding-left: 6px; }
.group-inventory .table-primary, .group-inventory .table-secondary { white-space: normal; overflow-wrap: anywhere; }
.group-inventory .resource-name { min-width: 0; }
.group-inventory .resource-name strong, .group-inventory .resource-name small { max-width: none; white-space: normal; overflow-wrap: anywhere; }
.group-inventory .mobile-resource-list { display: none; }
.group-mobile-card { min-width: 0; padding: 16px; border: 1px solid var(--gray-200); border-radius: 7px; background: var(--white); }
.group-mobile-card > header { display: flex; justify-content: space-between; align-items: flex-start; gap: 10px; }
.group-mobile-card dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--gray-100); }
.group-mobile-card dl div { min-width: 0; }
.group-mobile-card dl div:last-child { grid-column: 1 / -1; }
.group-mobile-card dt { display: flex; align-items: center; gap: 4px; color: var(--gray-500); font-size: 11px; }
.group-mobile-card dd { margin-top: 4px; color: var(--gray-800); font-size: 12px; white-space: normal; overflow-wrap: anywhere; }
.group-mobile-card footer { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin-top: 18px; }
.group-mobile-card footer > .integration-menu, .group-mobile-card footer .button { width: 100%; min-width: 0; }
@media (max-width: 1300px) {
  .group-table-wrap { display: none; }
  .group-inventory .mobile-resource-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
}
@media (max-width: 760px) {
  .group-inventory .mobile-resource-list { grid-template-columns: 1fr; gap: 10px; }
  .group-mobile-card { padding: 13px; border-radius: 4px; box-shadow: none; }
  .group-mobile-card footer { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
.integration-menu { position: relative; display: inline-flex; }
.integration-menu__toggle { gap: 6px; }
.integration-menu__items { position: fixed; z-index: 90; display: grid; max-height: calc(100vh - 24px); overflow-y: auto; padding: 5px; border: 1px solid var(--gray-200); background: var(--white, #fff); box-shadow: 0 10px 24px rgb(15 31 55 / 14%); }
.integration-menu__items button { padding: 9px 10px; border: 0; background: transparent; color: var(--navy-800); text-align: left; font-size: 12px; line-height: 1.35; cursor: pointer; }
.integration-menu__items button:hover, .integration-menu__items button:focus-visible { background: var(--gray-50); outline: none; }
</style>
