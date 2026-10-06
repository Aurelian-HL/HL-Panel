<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Pencil, Plus, RefreshCw, Search } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type Customer, type UserGroup } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import UserGroupEditor from '@/features/user-groups/UserGroupEditor.vue'
import { toast } from '@/composables/toast'
import { displayError } from '@/lib/displayFormatters'
const groups = ref<UserGroup[]>([])
const devices = ref<DeviceGroup[]>([])
const customers = ref<Customer[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const editor = ref(false)
const selected = ref<UserGroup | null>(null)
const filtered = computed(() => groups.value.filter((item) => `${item.name} ${item.description}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const deviceNames = (ids: string[]) => ids.map((id) => devices.value.find((device) => device.id === id)?.name ?? '设备组不可用').join('、') || '未授权'
const userCounts = computed(() => {
  const counts = new Map<string, number>()
  for (const customer of customers.value) counts.set(customer.user_group_id, (counts.get(customer.user_group_id) ?? 0) + 1)
  return counts
})
async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try { const [g, d, c] = await Promise.all([businessApi.userGroups(), businessApi.deviceGroups(), businessApi.customers()]); groups.value = g.items; devices.value = d.items; customers.value = c.items }
  catch (cause) { error.value = displayError(cause) } finally { loading.value = false }
}
function open(group: UserGroup | null): void { selected.value = group; editor.value = true }
async function saved(group: UserGroup): Promise<void> { editor.value = false; toast.success('用户组已保存', group.name); await load() }
onMounted(load)
</script>
<template>
  <div class="page-stack">
    <header class="page-heading"><h2>用户组</h2><div class="page-heading__actions"><button class="button button--secondary" :disabled="loading" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button><button class="button button--primary" :disabled="loading || !!error" @click="open(null)"><Plus :size="16" />添加用户组</button></div></header>
    <section class="toolbar"><label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索用户组名称或说明" /></label><span class="inventory-count">{{ groups.length }} 个用户组</span></section>
    <StatePanel v-if="loading && !groups.length" state="loading" title="正在读取用户组" />
    <StatePanel v-else-if="error && !groups.length" state="error" title="用户组加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!groups.length" state="empty" title="还没有用户组" message="先创建设备组，再为用户组授予入口组、落地出口组和入口直出权限。" />
    <StatePanel v-else-if="!filtered.length" state="empty" title="没有匹配的用户组" />
    <div v-else class="business-inventory">
      <div class="business-desktop table-wrap"><table class="data-table business-table user-group-table">
        <thead><tr><th>ID</th><th>名称</th><th>用户数量</th><th>入口转发组</th><th>落地出口组</th><th>入口直出</th><th>操作</th></tr></thead>
        <tbody><tr v-for="group in filtered" :key="group.id">
          <td><span class="table-id" :title="group.id">{{ group.id }}</span></td>
          <td><strong class="table-primary">{{ group.name }}</strong><small v-if="group.description" class="table-secondary">{{ group.description }}</small></td>
          <td>{{ userCounts.get(group.id) ?? 0 }}</td>
          <td>{{ deviceNames(group.allowed_entry_group_ids) }}</td>
          <td>{{ deviceNames(group.allowed_exit_group_ids) }}</td>
          <td><span class="business-status" :class="{ 'business-status--muted': !group.allow_direct }">{{ group.allow_direct ? '允许' : '禁止' }}</span></td>
          <td><button class="button button--quiet" @click="open(group)"><Pencil :size="14" />编辑</button></td>
        </tr></tbody>
      </table></div>
      <div class="business-mobile"><article v-for="group in filtered" :key="group.id" class="business-card">
        <header><div><h3>{{ group.name }}</h3><p>{{ userCounts.get(group.id) ?? 0 }} 个用户<span v-if="group.description"> · {{ group.description }}</span></p></div><button class="button button--quiet" @click="open(group)"><Pencil :size="14" />编辑</button></header>
        <dl><div><dt>入口转发组</dt><dd>{{ deviceNames(group.allowed_entry_group_ids) }}</dd></div><div><dt>落地出口组</dt><dd>{{ deviceNames(group.allowed_exit_group_ids) }}</dd></div><div><dt>入口直出</dt><dd>{{ group.allow_direct ? '允许' : '禁止' }}</dd></div><div><dt>ID</dt><dd>{{ group.id }}</dd></div></dl>
      </article></div>
    </div>
    <p v-if="error && groups.length" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>
    <UserGroupEditor v-if="editor" :group="selected" :devices="devices" @close="editor = false" @saved="saved" />
  </div>
</template>
