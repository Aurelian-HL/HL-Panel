<script setup lang="ts">
import { Copy, Pause, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'

import { customerApi, type CustomerRule, type CustomerRuleDetail, type CustomerRuleOptions } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import StatusPill from '@/components/StatusPill.vue'
import CustomerRuleEditor from '@/features/rules/CustomerRuleEditor.vue'
import { ingressProtocolLabel, target } from '@/lib/format'

const rules = ref<CustomerRule[]>([])
const loading = ref(true)
const error = ref('')
const options = ref<CustomerRuleOptions | null>(null)
const editorRule = ref<CustomerRuleDetail | null>(null)
const editorOpen = ref(false)
const copying = ref(false)
const actionBusy = ref(false)
const actionError = ref('')
const deleting = ref<CustomerRule | null>(null)
const query = ref('')
const filteredRules = computed(() => {
  const term = query.value.trim().toLowerCase()
  if (!term) return rules.value
  return rules.value.filter((rule) => [
    rule.name,
    rule.protocol,
    rule.connect_host,
    String(rule.listen_port),
    rule.route_description,
    ...rule.targets.flatMap((item) => [item.host, String(item.port), target(item.host, item.port)]),
  ].some((value) => value.toLowerCase().includes(term)))
})

async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try { rules.value = await customerApi.rules() }
  catch (cause) { error.value = cause instanceof Error ? cause.message : '线路信息加载失败' }
  finally { loading.value = false }
}

async function openEditor(rule: CustomerRule | null, copy = false): Promise<void> {
  if (actionBusy.value) return
  actionBusy.value = true; actionError.value = ''
  try {
    const [available, detail] = await Promise.all([customerApi.ruleOptions(), rule ? customerApi.rule(rule.id) : Promise.resolve(null)])
    options.value = available; editorRule.value = detail; copying.value = copy; editorOpen.value = true
  } catch (cause) { actionError.value = cause instanceof Error ? cause.message : '无法打开规则编辑器' }
  finally { actionBusy.value = false }
}
async function mutate(rule: CustomerRule, operation: 'pause' | 'resume' | 'delete'): Promise<void> {
  if (actionBusy.value) return
  actionBusy.value = true; actionError.value = ''
  try {
    const detail = await customerApi.rule(rule.id)
    await customerApi.batchRules(operation, detail)
    deleting.value = null
    await load()
  } catch (cause) { actionError.value = cause instanceof Error ? cause.message : '规则操作失败' }
  finally { actionBusy.value = false }
}
async function saved(): Promise<void> { editorOpen.value = false; await load() }
onMounted(load)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading"><h1>转发规则</h1><div class="rule-heading-actions"><button class="button button-secondary" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" />刷新</button><button class="button button-primary" :disabled="loading || actionBusy" @click="openEditor(null)"><Plus :size="15" />添加规则</button></div></header>
    <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
    <div class="rule-toolbar"><label class="search-field"><Search :size="15" /><span class="sr-only">搜索规则</span><input v-model="query" type="search" placeholder="搜索名称、端口、地址" /></label></div>
    <StatePanel v-if="loading && !rules.length" state="loading" title="正在读取线路信息" />
    <StatePanel v-else-if="error && !rules.length" state="error" title="线路信息加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!rules.length" state="empty" title="暂无转发规则" />

    <StatePanel v-else-if="!filteredRules.length" state="empty" title="没有匹配的转发规则" />

    <section v-else class="rule-inventory">
      <div class="data-table-scroll"><table class="data-table"><thead><tr><th>名称</th><th>状态</th><th>协议</th><th>连接信息</th><th>转发方式</th><th>目标地址</th><th>操作</th></tr></thead><tbody><tr v-for="rule in filteredRules" :key="rule.id"><td><strong>{{ rule.name }}</strong></td><td><div class="status-stack"><StatusPill :status="rule.status" :paused="rule.paused" /><small>{{ rule.deployed ? '已同步' : '未同步' }}</small></div></td><td><strong>{{ ingressProtocolLabel(rule.ingress_protocol, rule.protocol) }}</strong><small class="table-secondary">{{ rule.protocol.toUpperCase() }} 传输</small></td><td class="mono-cell">{{ rule.connect_host }}:{{ rule.listen_port }}</td><td>{{ rule.route_description }}</td><td class="target-cell">{{ rule.targets.map(item => target(item.host, item.port)).join('、') }}</td><td class="rule-actions"><button class="icon-button" title="编辑规则" aria-label="编辑规则" :disabled="actionBusy" @click="openEditor(rule)"><Pencil :size="14" /></button><button class="icon-button" title="复制规则" aria-label="复制规则" :disabled="actionBusy" @click="openEditor(rule, true)"><Copy :size="14" /></button><button class="icon-button" :title="rule.paused ? '恢复规则' : '暂停规则'" :aria-label="rule.paused ? '恢复规则' : '暂停规则'" :disabled="actionBusy" @click="mutate(rule, rule.paused ? 'resume' : 'pause')"><Play v-if="rule.paused" :size="14" /><Pause v-else :size="14" /></button><button class="icon-button" title="删除规则" aria-label="删除规则" :disabled="actionBusy" @click="deleting = rule"><Trash2 :size="14" /></button></td></tr></tbody></table></div>
      <div class="mobile-rule-list">
      <article v-for="rule in filteredRules" :key="rule.id" class="service-item">
        <header><div><h2>{{ rule.name }}</h2><p>{{ ingressProtocolLabel(rule.ingress_protocol, rule.protocol) }} · {{ rule.protocol.toUpperCase() }} · {{ rule.connect_host }}:{{ rule.listen_port }}</p></div><StatusPill :status="rule.status" :paused="rule.paused" /></header>
        <dl><div><dt>同步状态</dt><dd>{{ rule.deployed ? '已同步' : '未同步' }}</dd></div><div><dt>连接地址</dt><dd>{{ rule.connect_host }}:{{ rule.listen_port }}</dd></div><div><dt>转发方式</dt><dd>{{ rule.route_description }}</dd></div><div><dt>目标地址</dt><dd>{{ rule.targets.map(item => target(item.host, item.port)).join('、') }}</dd></div></dl><footer class="rule-actions"><button class="button button-secondary" :disabled="actionBusy" @click="openEditor(rule)"><Pencil :size="14" />编辑</button><button class="button button-secondary" :disabled="actionBusy" @click="openEditor(rule, true)"><Copy :size="14" />复制</button><button class="button button-secondary" :disabled="actionBusy" @click="mutate(rule, rule.paused ? 'resume' : 'pause')"><Play v-if="rule.paused" :size="14" /><Pause v-else :size="14" />{{ rule.paused ? '恢复' : '暂停' }}</button><button class="button button-secondary" :disabled="actionBusy" @click="deleting = rule"><Trash2 :size="14" />删除</button></footer>
      </article>
      </div>
    </section>

    <p v-if="error && rules.length" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>
    <CustomerRuleEditor v-if="editorOpen && options" :options="options" :rule="editorRule" :copy="copying" @close="editorOpen = false" @saved="saved" />
    <Teleport to="body"><div v-if="deleting" class="modal-backdrop"><section class="modal" role="alertdialog" aria-modal="true" aria-label="确认删除规则"><header><h2>删除规则</h2></header><p class="rule-delete-message">确定删除“{{ deleting.name }}”？此操作不可撤销。</p><footer><button class="button button-secondary" :disabled="actionBusy" @click="deleting = null">取消</button><button class="button button-primary" :disabled="actionBusy" @click="mutate(deleting, 'delete')">确认删除</button></footer></section></div></Teleport>
  </div>
</template>
