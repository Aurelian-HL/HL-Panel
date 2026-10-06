<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Plus, RefreshCw, Search } from '@lucide/vue'
import { businessApi, type Customer, type UserGroup } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import CustomerEditor from '@/features/customers/CustomerEditor.vue'
import CustomerInventory from '@/features/customers/CustomerInventory.vue'
import { toast } from '@/composables/toast'
import { displayError } from '@/lib/displayFormatters'
const customers = ref<Customer[]>([])
const groups = ref<UserGroup[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const filter = ref('all')
const editor = ref(false)
const selected = ref<Customer | null>(null)
const filtered = computed(() => customers.value.filter((item) => `${item.username} ${item.display_name}`.toLowerCase().includes(query.value.trim().toLowerCase()) && (filter.value === 'all' || item.effective_status === filter.value)))
async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try { const [c, g] = await Promise.all([businessApi.customers(), businessApi.userGroups()]); customers.value = c.items; groups.value = g.items }
  catch (cause) { error.value = displayError(cause) } finally { loading.value = false }
}
function open(customer: Customer | null): void { selected.value = customer; editor.value = true }
async function saved(customer: Customer): Promise<void> { editor.value = false; toast.success('用户已保存', customer.display_name || customer.username); await load() }
onMounted(load)
</script>
<template>
  <div class="page-stack">
    <header class="page-heading"><h2>用户管理</h2><div class="page-heading__actions"><button class="button button--secondary" :disabled="loading" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button><button class="button button--primary" :disabled="loading || !!error" @click="open(null)"><Plus :size="16" />添加用户</button></div></header>
    <section class="toolbar business-toolbar"><label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索账号或显示名称" /></label><label class="toolbar-select"><span>授权状态</span><select v-model="filter"><option value="all">全部用户</option><option value="active">可用</option><option value="disabled">已停用</option><option value="expired">已到期</option><option value="quota_exhausted">额度已用完</option></select></label></section>
    <StatePanel v-if="loading && !customers.length" state="loading" title="正在读取用户" />
    <StatePanel v-else-if="error && !customers.length" state="error" title="用户加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!customers.length" state="empty" title="暂无用户" message="点击“添加用户”创建账号。" />
    <StatePanel v-else-if="!filtered.length" state="empty" title="没有匹配的用户" message="调整搜索或授权状态筛选。" />
    <CustomerInventory v-else :customers="filtered" :groups="groups" @edit="open" />
    <p v-if="error && customers.length" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>
    <CustomerEditor v-if="editor" :customer="selected" :groups="groups" @close="editor = false" @saved="saved" />
  </div>
</template>
