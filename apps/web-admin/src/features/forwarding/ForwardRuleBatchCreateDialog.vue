<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { LoaderCircle, RefreshCw } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type ForwardRuleInput, type GroupNetwork, type RuleBatchCreateResult, type RuleGroup } from '@/api/business'
import { ApiError } from '@/api/http'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'
import { networkAllowsDirect } from '@/lib/forwardingRoutes'
import { batchCreateRule, parseBatchCreate } from './batchCreate'
import { useRealityTargetProbe } from './useRealityTargetProbe'

const props = defineProps<{ devices: DeviceGroup[]; networks: GroupNetwork[]; ruleGroups: RuleGroup[] }>()
const emit = defineEmits<{ close: []; saved: [result: RuleBatchCreateResult]; unauthorized: [] }>()
const content = ref('')
const busy = ref(false)
const error = ref('')
const common = reactive<ForwardRuleInput>({ name: '', rule_group_id: '', entry_group_id: '', exit_group_id: '', egress_mode: 'DIRECT',
  ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', vless_outbound_mode: 'SOCKS5',
  reality_server_name: '', reality_destination: '', protocol: 'tcp', listen_port: 0, targets: [], selection_policy: 'round_robin',
  paused: false, description: '', revision: 0 })
const enabled = computed(() => true)
const { loadingRealityTargets, probeError, currentProbe, loadCreationRealityTarget, randomizeReality } = useRealityTargetProbe(common, enabled, true)
onMounted(loadCreationRealityTarget)
const parsed = computed(() => parseBatchCreate(content.value))
const entries = computed(() => props.devices.filter(item => item.kind === 'ENTRY' &&
  !props.networks.some(network => network.group_id === item.id && !networkAllowsDirect(network))))
const canSubmit = computed(() => !busy.value && !loadingRealityTargets.value && !!common.entry_group_id &&
  !probeError.value && currentProbe.value?.compatible === true &&
  !!common.reality_server_name && !!common.reality_destination && parsed.value.rows.length > 0 && !parsed.value.issues.length)
const mutationKey = useMutationKey()

async function submit(): Promise<void> {
  if (!canSubmit.value) return
  error.value = ''
  busy.value = true
  const input = { rules: parsed.value.rows.map(row => batchCreateRule(row, common)) }
  try { emit('saved', await businessApi.batchCreateRules(input, mutationKey(input))) }
  catch (cause) {
    if (cause instanceof ApiError && cause.status === 401) emit('unauthorized')
    else error.value = displayError(cause)
  } finally { busy.value = false }
}
</script>

<template>
  <BaseModal title="批量创建规则" description="VLESS + Reality · SOCKS5 出站 · 自动分配端口" width="large" :close-disabled="busy" @close="emit('close')">
    <form id="forward-rule-batch-create" class="form-stack" @submit.prevent="submit">
      <fieldset class="batch-fields" :disabled="busy">
        <div class="field-grid">
          <label class="field"><span>入口设备组</span><select v-model="common.entry_group_id" required><option value="">请选择</option><option v-for="item in entries" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
          <label class="field"><span>规则分组</span><select v-model="common.rule_group_id"><option value="">未分组</option><option v-for="item in ruleGroups" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        </div>
        <label class="field"><span>昵称与 SK5</span><textarea v-model="content" rows="7" class="batch-source" spellcheck="false" autocomplete="off" placeholder="192.0.2.10:1080:账号:密码&#10;客户昵称 192.0.2.11:1080:账号:密码" /><small class="field-help">每行一条，最多 500 条。昵称可留空，默认使用 SK5 的 IP；填写昵称时用空格或制表符隔开。</small></label>
        <div class="batch-reality"><span>{{ loadingRealityTargets ? '正在检测 Reality 目标…' : !probeError && currentProbe?.compatible ? 'Reality 目标已检测：' + common.reality_server_name : 'Reality 目标尚未就绪' }}</span><button type="button" class="button button--secondary" :disabled="loadingRealityTargets" @click="randomizeReality"><RefreshCw :size="13" :class="{ spin: loadingRealityTargets }" />重新检测</button></div>
        <p v-if="probeError" class="inline-warning" role="status">{{ probeError }}</p>
      </fieldset>
      <div v-if="parsed.issues.length" class="form-error" role="alert"><p v-for="issue in parsed.issues.slice(0, 5)" :key="issue.line">第 {{ issue.line }} 行：{{ issue.message }}</p><p v-if="parsed.issues.length > 5">另有 {{ parsed.issues.length - 5 }} 行需要修正</p><p>请修正后再提交，本批尚未保存。</p></div>
      <div v-else-if="parsed.rows.length" class="batch-preview" aria-live="polite"><p class="batch-count">已识别 {{ parsed.rows.length }} 条规则</p><div v-for="row in parsed.rows.slice(0, 5)" :key="row.line" class="batch-row"><strong>{{ row.name }}</strong><span>{{ row.host }}:{{ row.port }}</span></div><p v-if="parsed.rows.length > 5" class="field-help">其余 {{ parsed.rows.length - 5 }} 条已识别</p></div>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button type="button" class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button type="submit" form="forward-rule-batch-create" class="button button--primary" :disabled="!canSubmit"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '正在保存…' : '创建 ' + parsed.rows.length + ' 条规则' }}</button></template>
  </BaseModal>
</template>

<style scoped>
.batch-fields { display: grid; gap: 16px; min-width: 0; margin: 0; padding: 0; border: 0; }
.batch-source { min-height: 160px; font-family: Consolas, monospace; }
.batch-reality, .batch-row { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.batch-reality { color: var(--ny-muted); font-size: 12px; }
.batch-preview { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--ny-border); border-radius: 6px; }
.batch-count { margin: 0; color: var(--ny-text); font-size: 13px; }
.batch-row { font-size: 12px; }
.batch-row strong { overflow-wrap: anywhere; }
.batch-row span { flex-shrink: 0; color: var(--ny-muted); }
@media (max-width: 640px) { .batch-reality { align-items: flex-start; flex-direction: column; } .batch-row { flex-wrap: wrap; gap: 4px; } }
</style>
