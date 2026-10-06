<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { X } from '@lucide/vue'

import { customerApi, type CustomerRuleDetail, type CustomerRuleInput, type CustomerRuleOptions } from '@/api'
import { formatTargetLines, parseTargetLines } from './targetLines'

const props = defineProps<{ options: CustomerRuleOptions; rule: CustomerRuleDetail | null; copy?: boolean }>()
const emit = defineEmits<{ close: []; saved: [] }>()
function editableInput(rule: CustomerRuleDetail): CustomerRuleInput {
  return {
    name: rule.name,
    rule_group_id: rule.rule_group_id,
    entry_group_id: rule.entry_group_id,
    exit_group_id: rule.exit_group_id,
    egress_mode: rule.egress_mode,
    ingress_protocol: rule.ingress_protocol,
    protocol: rule.protocol,
    vless_flow: rule.vless_flow,
    reality_server_name: rule.reality_server_name,
    reality_public_key: rule.reality_public_key,
    reality_short_id: rule.reality_short_id,
    reality_destination: rule.reality_destination,
    listen_port: rule.listen_port,
    targets: rule.targets.map((item) => ({ ...item })),
    selection_policy: rule.selection_policy,
    accept_proxy_protocol: rule.accept_proxy_protocol,
    send_proxy_protocol: rule.send_proxy_protocol,
    speed_limit_mbps: rule.speed_limit_mbps,
    ip_limit: rule.ip_limit,
    connection_limit: rule.connection_limit,
    paused: rule.paused,
    description: rule.description,
    revision: rule.revision,
  }
}
const form = reactive<CustomerRuleInput>(props.rule ? {
  ...editableInput(props.rule),
  ...(props.copy ? {
    name: `${props.rule.name} - 副本`, listen_port: 0, revision: 0, paused: true,
    reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '',
  } : {}),
} : {
  name: '', rule_group_id: '', entry_group_id: '', exit_group_id: '', egress_mode: 'DIRECT',
  ingress_protocol: 'tcp', protocol: 'tcp', vless_flow: '', reality_server_name: '', reality_public_key: '',
  reality_short_id: '', reality_destination: '', listen_port: 0, targets: [],
  selection_policy: 'round_robin', accept_proxy_protocol: false, send_proxy_protocol: 0,
  speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, paused: false, description: '', revision: 0,
})
const targetText = ref(formatTargetLines(props.rule?.targets ?? []))
const busy = ref(false)
const error = ref('')
const isVless = computed(() => form.ingress_protocol === 'vless_reality')
const isSocks5 = computed(() => form.ingress_protocol === 'socks5')
const entries = computed(() => props.options.entry_groups.filter((item) => isVless.value ? item.allow_direct : item.allow_direct || item.allowed_exit_group_ids.length > 0))
const entry = computed(() => props.options.entry_groups.find((item) => item.id === form.entry_group_id))
const exits = computed(() => props.options.exit_groups.filter((item) => entry.value?.allowed_exit_group_ids.includes(item.id)))
const portHint = computed(() => entry.value?.port_ranges.map((item) => `${item.start}-${item.end}`).join('、') ?? '')
const routeChoice = computed({
  get: () => form.egress_mode === 'DIRECT' ? 'direct' : form.exit_group_id ? `exit:${form.exit_group_id}` : '',
  set: (value: string) => {
    form.egress_mode = value === 'direct' ? 'DIRECT' : 'EXIT_GROUP'
    form.exit_group_id = value.startsWith('exit:') ? value.slice(5) : ''
  },
})

watch(() => form.ingress_protocol, (value) => {
  form.protocol = value === 'udp' ? 'udp' : 'tcp'
  form.vless_flow = value === 'vless_reality' ? 'xtls-rprx-vision' : ''
  if (value === 'vless_reality') {
    form.egress_mode = 'DIRECT'; form.exit_group_id = ''; form.send_proxy_protocol = 0; form.accept_proxy_protocol = false
    if (entry.value && !entry.value.allow_direct) form.entry_group_id = ''
  }
  if (value !== 'vless_reality') { form.reality_server_name = ''; form.reality_public_key = ''; form.reality_short_id = ''; form.reality_destination = '' }
  if (value === 'udp') { form.speed_limit_mbps = 0; form.ip_limit = 0; form.connection_limit = 0; form.accept_proxy_protocol = false; form.send_proxy_protocol = 0 }
})
watch(() => form.entry_group_id, () => {
  form.exit_group_id = ''
  form.egress_mode = entry.value?.allow_direct ? 'DIRECT' : 'EXIT_GROUP'
})

async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (!form.name.trim() || !entry.value) { error.value = '请填写名称并选择入口组'; return }
  if (form.egress_mode === 'DIRECT' && !entry.value.allow_direct) { error.value = '该入口组不支持直出'; return }
  if (form.egress_mode === 'EXIT_GROUP' && !exits.value.some((item) => item.id === form.exit_group_id)) { error.value = '请选择出口组'; return }
  let targets: CustomerRuleInput['targets']
  try { targets = parseTargetLines(targetText.value) }
  catch (cause) { error.value = cause instanceof Error ? cause.message : '目标地址格式无效'; return }
  if (isVless.value && targets.length !== 1) { error.value = 'VLESS 规则只能填写一个目标地址'; return }
  if (!Number.isInteger(form.listen_port) || form.listen_port < 0 || form.listen_port > 65535) { error.value = '监听端口无效'; return }
  busy.value = true
  try {
    await customerApi.saveRule({ ...form, name: form.name.trim(), targets, exit_group_id: form.egress_mode === 'DIRECT' ? '' : form.exit_group_id, description: form.description.trim() }, props.copy ? null : props.rule?.id ?? null)
    emit('saved')
  } catch (cause) { error.value = cause instanceof Error ? cause.message : '规则保存失败' }
  finally { busy.value = false }
}
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" role="presentation">
      <section class="modal customer-rule-modal" role="dialog" aria-modal="true" aria-labelledby="customer-rule-title">
        <header><h2 id="customer-rule-title">{{ copy ? '复制规则' : rule ? '编辑规则' : '添加规则' }}</h2><button class="icon-button" type="button" aria-label="关闭" title="关闭" :disabled="busy" @click="emit('close')"><X :size="16" /></button></header>
        <form id="customer-rule-form" @submit.prevent="save">
          <label><span>名称</span><input v-model="form.name" maxlength="128" required /></label>
          <label><span>入口组</span><select v-model="form.entry_group_id" required><option value="" disabled>请选择入口组</option><option v-for="item in entries" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
          <div class="rule-routing-row">
            <fieldset class="rule-choice"><legend>接入协议</legend><div class="segmented"><button type="button" :class="{ active: form.ingress_protocol === 'tcp' }" @click="form.ingress_protocol = 'tcp'">NY TCP</button><button type="button" :class="{ active: form.ingress_protocol === 'udp' }" @click="form.ingress_protocol = 'udp'">NY UDP</button><button type="button" title="NY SOCKS5 客户端入口" :class="{ active: isSocks5 }" @click="form.ingress_protocol = 'socks5'">NY SOCKS5</button><button type="button" title="VLESS + Reality + Vision" :class="{ active: isVless }" @click="form.ingress_protocol = 'vless_reality'">VLESS + Reality + Vision</button></div></fieldset>
            <label><span>出口组</span><select v-model="routeChoice" :disabled="!entry" required><option value="" disabled>请选择出口组</option><option v-if="entry?.allow_direct" value="direct">入口直出（不使用出口组）</option><option v-for="item in isVless ? [] : exits" :key="item.id" :value="`exit:${item.id}`">经 {{ item.name }} 转发</option></select></label>
          </div>
          <small v-if="isVless" class="rule-protocol-note">Reality 密钥、Short ID 与 Vision 参数由平台生成；当前仅支持入口直出。</small>
          <label><span>监听端口</span><input v-model.number="form.listen_port" type="number" min="0" max="65535" required /><small v-if="entry">{{ entry.connect_host }} · {{ portHint }}</small></label>
          <label class="rule-targets"><span>目标地址</span><textarea v-model="targetText" rows="3" :placeholder="'example.com:443\n[2001:db8::1]:443'" spellcheck="false" /></label>
          <details class="rule-advanced"><summary>高级选项</summary><div class="rule-advanced-fields">
            <label><span>规则分组</span><select v-model="form.rule_group_id"><option value="">未分组</option><option v-for="item in options.rule_groups" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
            <label><span>目标选择</span><select v-model="form.selection_policy"><option value="round_robin">轮询</option><option value="random">随机</option><option value="ip_hash">IP 哈希</option><option value="least_load">最小连接数</option><option value="failover">故障转移</option></select></label>
            <label><span>规则限速 (Mbps)</span><input v-model.number="form.speed_limit_mbps" type="number" min="0" :disabled="form.protocol === 'udp'" /></label>
            <label><span>IP 限制</span><input v-model.number="form.ip_limit" type="number" min="0" :disabled="form.protocol === 'udp'" /></label>
            <label><span>连接数限制</span><input v-model.number="form.connection_limit" type="number" min="0" :disabled="form.protocol === 'udp'" /></label>
            <label><span>备注</span><textarea v-model="form.description" rows="2" maxlength="1024" /></label>
          </div></details>
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        </form>
        <footer><button class="button button-secondary" type="button" :disabled="busy" @click="emit('close')">取消</button><button class="button button-primary" type="submit" form="customer-rule-form" :disabled="busy">{{ busy ? '保存中' : '保存' }}</button></footer>
      </section>
    </div>
  </Teleport>
</template>
