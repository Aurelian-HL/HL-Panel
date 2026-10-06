<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { FileUp, LoaderCircle, SearchCheck } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type ForwardImportFormat, type ForwardImportPreview, type ForwardTransferRequest, type GroupNetwork, type RuleGroup, type IngressProtocol } from '@/api/business'
import { ApiError } from '@/api/http'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ devices: DeviceGroup[]; networks: GroupNetwork[]; ruleGroups: RuleGroup[] }>()
const emit = defineEmits<{ close: []; imported: [count: number]; unauthorized: [] }>()
const format = ref<ForwardImportFormat>('auto')
const content = ref('')
const mapping = reactive({ rule_group_id: '', entry_group_id: '', egress_mode: '' as '' | 'DIRECT' | 'EXIT_GROUP', exit_group_id: '', ingress_protocol: '' as '' | IngressProtocol, protocol: '' as '' | 'tcp' | 'udp', selection_policy: '' as '' | 'round_robin' | 'random' | 'ip_hash' | 'least_load' | 'failover' })
const preview = ref<ForwardImportPreview | null>(null)
const previewSignature = ref('')
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
const entryGroups = computed(() => props.devices.filter((item) => item.kind === 'ENTRY'))
const exitGroups = computed(() => props.devices.filter((item) => item.kind === 'EXIT'))
const detectedLegacy = computed(() => format.value === 'ny_text' || (format.value === 'auto' && !content.value.trimStart().startsWith('{')))
const availableEntries = entryGroups
const availableExits = exitGroups
const currentNetwork = computed(() => props.networks.find((item) => item.group_id === mapping.entry_group_id))
const request = computed<ForwardTransferRequest>(() => ({
  format: format.value,
  content: content.value,
  mapping: {
    customer_id: '',
    rule_group_id: mapping.rule_group_id,
    entry_group_id: mapping.entry_group_id,
    egress_mode: mapping.egress_mode || (detectedLegacy.value ? 'DIRECT' : ''),
    exit_group_id: mapping.egress_mode === 'EXIT_GROUP' ? mapping.exit_group_id : '',
    ingress_protocol: mapping.ingress_protocol || (detectedLegacy.value ? (mapping.protocol === 'udp' ? 'udp' : 'tcp') : ''),
    protocol: mapping.protocol || (detectedLegacy.value ? 'tcp' : ''),
    selection_policy: mapping.selection_policy || (detectedLegacy.value ? 'round_robin' : ''),
  },
}))
const signature = computed(() => JSON.stringify(request.value))
const globalIssues = computed(() => preview.value?.global_issues ?? [])
const canCommit = computed(() => Boolean(preview.value && previewSignature.value === signature.value && preview.value.total > 0 && preview.value.invalid === 0 && globalIssues.value.length === 0))

watch(() => mapping.entry_group_id, () => { mapping.exit_group_id = '' })
watch(() => mapping.egress_mode, (mode) => { if (mode !== 'EXIT_GROUP') mapping.exit_group_id = '' })
watch(() => mapping.ingress_protocol, (protocol) => {
  if (protocol === 'vless_reality') {
    mapping.egress_mode = 'DIRECT'
    mapping.exit_group_id = ''
    mapping.protocol = 'tcp'
  } else if (protocol === 'socks5') {
    // SOCKS5 is a distinct customer protocol carried over TCP.
    mapping.protocol = 'tcp'
  } else if (protocol === 'udp') {
    mapping.protocol = 'udp'
  }
})
watch(detectedLegacy, (legacy) => {
  if (legacy) {
    if (!mapping.egress_mode) mapping.egress_mode = 'DIRECT'
    if (!mapping.protocol) mapping.protocol = 'tcp'
    if (!mapping.ingress_protocol) mapping.ingress_protocol = mapping.protocol === 'udp' ? 'udp' : 'tcp'
    if (!mapping.selection_policy) mapping.selection_policy = 'round_robin'
    return
  }
  mapping.egress_mode = ''
    mapping.protocol = ''
    mapping.ingress_protocol = ''
  mapping.selection_policy = ''
}, { immediate: true })

async function readFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  error.value = ''
  if (file.size > 2 * 1024 * 1024) { error.value = '文件不能超过 2 MiB'; input.value = ''; return }
  try { content.value = await file.text() } catch { error.value = '文件读取失败，请重新选择' }
  input.value = ''
}

async function runPreview(): Promise<void> {
  if (busy.value) return
  error.value = ''; busy.value = true
  const submittedRequest = request.value
  const submittedSignature = signature.value
  try { preview.value = await businessApi.previewRuleImport(submittedRequest); previewSignature.value = submittedSignature }
  catch (cause) { if (cause instanceof ApiError && cause.status === 401) emit('unauthorized'); else error.value = displayError(cause); preview.value = null; previewSignature.value = '' }
  finally { busy.value = false }
}

async function commit(): Promise<void> {
  if (busy.value || !canCommit.value || !preview.value) return
  error.value = ''; busy.value = true
  try {
    const result = await businessApi.importRules(request.value, mutationKey(request.value))
    emit('imported', result.created + result.updated)
  } catch (cause) { if (cause instanceof ApiError && cause.status === 401) emit('unauthorized'); else error.value = displayError(cause) }
  finally { busy.value = false }
}

function rowIssues(row: ForwardImportPreview['rows'][number]): string {
  return (row.issues ?? []).map((item) => item.message).join('；')
}
</script>

<template>
  <BaseModal title="导入转发规则" description="支持 NY 四列文本和版本化 JSON，最多 500 条" width="large" :close-disabled="busy" @close="emit('close')">
    <form id="forward-rule-import" class="form-stack transfer-form" @submit.prevent="commit">
      <div class="field-grid">
        <label class="field"><span>文件格式</span><select v-model="format"><option value="auto">自动识别</option><option value="ny_text">NY 文本</option><option value="json_v1">版本化 JSON</option></select></label>
        <label class="field file-picker"><span>读取文件</span><span class="button button--secondary"><FileUp :size="14" />选择 .txt / .json<input type="file" accept=".txt,.json,text/plain,application/json" @change="readFile" /></span></label>
      </div>
      <label class="field"><span>导入内容</span><textarea v-model="content" class="transfer-source" rows="7" spellcheck="false" placeholder="NY 文本：名称#监听端口#目标地址#目标端口&#10;监听端口可留空自动分配" /></label>
      <details class="ny-advanced" open><summary>资源映射</summary><div class="field-grid transfer-mapping">
        <label class="field"><span>规则分组</span><select v-model="mapping.rule_group_id"><option value="">{{ detectedLegacy ? '未分组' : '沿用文件' }}</option><option v-for="item in ruleGroups" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label class="field"><span>入口转发组{{ detectedLegacy ? '' : '（留空沿用 JSON）' }}</span><select v-model="mapping.entry_group_id" :required="detectedLegacy"><option value="">{{ detectedLegacy ? '请选择' : '沿用文件' }}</option><option v-for="item in availableEntries" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label class="field"><span>转发路径</span><select v-model="mapping.egress_mode"><option v-if="!detectedLegacy" value="">沿用文件</option><option value="DIRECT">入口直出</option><option value="EXIT_GROUP" :disabled="mapping.ingress_protocol === 'vless_reality'">经出口组</option></select></label>
        <label v-if="mapping.egress_mode === 'EXIT_GROUP'" class="field"><span>落地出口组</span><select v-model="mapping.exit_group_id" required><option value="">请选择</option><option v-for="item in availableExits.filter(exit => currentNetwork?.allowed_exit_group_ids.includes(exit.id))" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label class="field"><span>协议</span><select v-model="mapping.protocol"><option v-if="!detectedLegacy" value="">沿用文件</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
        <label class="field"><span>客户接入协议</span><select v-model="mapping.ingress_protocol"><option v-if="!detectedLegacy" value="">沿用文件</option><option value="tcp">NY TCP</option><option value="udp">NY UDP</option><option value="socks5">NY SOCKS5</option><option value="vless_reality">VLESS + Reality + Vision</option></select><small class="field-help">四种客户接入协议会保留各自标识；SOCKS5 使用 TCP 传输但不会转换为 NY TCP。</small></label>
        <label class="field"><span>目标选择方式</span><select v-model="mapping.selection_policy"><option v-if="!detectedLegacy" value="">沿用文件</option><option value="round_robin">轮询</option><option value="random">随机</option><option value="ip_hash">IP 哈希</option><option value="least_load">最小连接数</option><option value="failover">故障转移</option></select></label>
      </div></details>
      <div class="transfer-preview-action"><button class="button button--secondary" type="button" :disabled="busy || !content.trim() || (detectedLegacy && !mapping.entry_group_id)" @click="runPreview"><LoaderCircle v-if="busy" :size="14" class="spin" /><SearchCheck v-else :size="14" />{{ busy ? '检查中' : '预览并检查' }}</button><span v-if="preview">共 {{ preview.total }} 条，{{ preview.valid }} 条可导入，{{ preview.invalid }} 条错误</span></div>
      <div v-if="globalIssues.length" class="form-error" role="alert"><p v-for="item in globalIssues" :key="item.code">{{ item.message }}</p></div>
      <div v-if="preview?.rows.length" class="transfer-preview" aria-live="polite">
        <article v-for="row in preview.rows" :key="`${row.line}:${row.source_id ?? row.request.name}`" class="transfer-row" :class="{ 'transfer-row--error': (row.issues?.length ?? 0) > 0 }">
          <div><strong>第 {{ row.line }} 行 · {{ row.request.name || '未命名规则' }}</strong><span v-if="!(row.issues?.length)">{{ row.action === 'update' ? '更新' : '新建' }} · {{ row.request.protocol.toUpperCase() }} :{{ row.resolved_listen_port }}</span><span v-else class="transfer-row__error">{{ rowIssues(row) }}</span></div>
          <small>{{ row.request.targets?.map(target => `${target.host}:${target.port}`).join('、') || '目标无效' }}</small>
        </article>
      </div>
      <p v-if="preview && previewSignature !== signature" class="inline-warning">内容或映射已变化，请重新预览。</p>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" type="button" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="submit" form="forward-rule-import" :disabled="busy || !canCommit"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '导入中' : `确认导入${preview ? ` ${preview.total} 条` : ''}` }}</button></template>
  </BaseModal>
</template>

<style scoped>
.transfer-form { gap: 13px; }
.file-picker .button { position: relative; width: 100%; justify-content: center; overflow: hidden; }
.file-picker input { position: absolute; inset: 0; opacity: 0; cursor: pointer; }
.transfer-source { min-height: 132px !important; font-family: Consolas, monospace; font-size: 12px !important; }
.transfer-mapping { margin-top: 11px; }
.transfer-preview-action { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.transfer-preview-action span { color: var(--ny-muted); font-size: 12px; }
.transfer-preview { display: flex; max-height: 240px; flex-direction: column; overflow: auto; border: 1px solid var(--ny-border); border-radius: 4px; }
.transfer-row { display: grid; gap: 4px; padding: 9px 11px; border-bottom: 1px solid var(--ny-border); }
.transfer-row:last-child { border-bottom: 0; }
.transfer-row > div { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.transfer-row strong { color: var(--ny-text); font-size: 12px; }
.transfer-row span, .transfer-row small { color: var(--ny-muted); font-size: 11px; }
.transfer-row--error { background: #fff8f8; }
.transfer-row .transfer-row__error { color: var(--red-700); text-align: right; }
@media (max-width: 760px) {
  .transfer-preview-action, .transfer-row > div { align-items: stretch; flex-direction: column; }
  .transfer-preview-action .button { width: 100%; }
  .transfer-row .transfer-row__error { text-align: left; }
}
</style>
