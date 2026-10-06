<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ArrowLeft, FolderTree, Pencil, Plus, RefreshCw, Search } from '@lucide/vue'
import { RouterLink } from 'vue-router'
import { businessApi, type ForwardRule, type RuleGroup } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import RuleGroupEditor from '@/features/rule-groups/RuleGroupEditor.vue'
import { toast } from '@/composables/toast'
import { displayError } from '@/lib/displayFormatters'

const groups = ref<RuleGroup[]>([])
const rules = ref<ForwardRule[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const editor = ref(false)
const selected = ref<RuleGroup | null>(null)
const filtered = computed(() => groups.value.filter((item) => `${item.name} ${item.description}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const counts = computed(() => {
  const result = new Map<string, number>()
  for (const rule of rules.value) if (rule.rule_group_id) result.set(rule.rule_group_id, (result.get(rule.rule_group_id) ?? 0) + 1)
  return result
})
async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try { const [groupResult, ruleResult] = await Promise.all([businessApi.ruleGroups(), businessApi.rules()]); groups.value = groupResult.items; rules.value = ruleResult.items }
  catch (cause) { error.value = displayError(cause) } finally { loading.value = false }
}
function open(group: RuleGroup | null): void { selected.value = group; editor.value = true }
async function saved(group: RuleGroup): Promise<void> { editor.value = false; toast.success('规则分组已保存', group.name); await load() }
onMounted(load)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading"><h2>规则分组</h2><div class="page-heading__actions"><RouterLink class="button button--secondary" to="/forward-rules"><ArrowLeft :size="15" />返回规则</RouterLink><button class="button button--secondary" :disabled="loading" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button><button class="button button--primary" :disabled="loading || !!error" @click="open(null)"><Plus :size="16" />添加分组</button></div></header>
    <section class="toolbar"><label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索分组名称或备注" /></label><span class="inventory-count">{{ groups.length }} 个分组</span></section>
    <StatePanel v-if="loading && !groups.length" state="loading" title="正在读取规则分组" />
    <StatePanel v-else-if="error && !groups.length" state="error" title="规则分组加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!groups.length" state="empty" title="还没有规则分组" message="创建分组后，可在转发规则页筛选或批量移动规则。" />
    <StatePanel v-else-if="!filtered.length" state="empty" title="没有匹配的规则分组" />
    <div v-else class="business-inventory rule-group-inventory"><div class="table-wrap"><table class="data-table business-table rule-group-table"><thead><tr><th>分组</th><th>规则数量</th><th>备注</th><th>操作</th></tr></thead><tbody><tr v-for="group in filtered" :key="group.id"><td><strong class="table-primary"><FolderTree :size="14" />{{ group.name }}</strong><small class="table-id" :title="group.id">{{ group.id }}</small></td><td>{{ counts.get(group.id) ?? 0 }}</td><td>{{ group.description || '—' }}</td><td><button class="button button--quiet" @click="open(group)"><Pencil :size="14" />编辑</button></td></tr></tbody></table></div></div>
    <p v-if="error && groups.length" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>
    <RuleGroupEditor v-if="editor" :group="selected" @close="editor = false" @saved="saved" />
  </div>
</template>
