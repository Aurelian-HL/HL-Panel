<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CheckCircle2, LoaderCircle, Send } from '@lucide/vue'

import { api, type CreateGroupRevisionResponse, type DeviceGroup } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ group: DeviceGroup }>()
const emit = defineEmits<{ close: []; published: [revision: number] }>()
const engine = ref<'xray' | 'gost'>('xray')
const configText = ref(JSON.stringify({ schema_version: 1, services: [] }, null, 2))
const idempotencyKey = ref(crypto.randomUUID())
const submitting = ref(false)
const errorMessage = ref('')
const result = ref<CreateGroupRevisionResponse | null>(null)

const nextRevision = computed(() => props.group.current_generation + 1)

watch([engine, configText], () => {
  if (!submitting.value && !result.value) idempotencyKey.value = crypto.randomUUID()
})

function parseConfig(): Record<string, unknown> {
  let value: unknown
  try {
    value = JSON.parse(configText.value)
  } catch {
    throw new Error('配置内容不是有效的 JSON')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('配置内容必须是 JSON 对象')
  const config = value as Record<string, unknown>
  if (!Number.isInteger(config.schema_version) || Number(config.schema_version) < 1) throw new Error('schema_version 必须是正整数')
  if (!Array.isArray(config.services)) throw new Error('services 必须是数组')
  return config
}

async function submit(): Promise<void> {
  errorMessage.value = ''
  let config: Record<string, unknown>
  try {
    config = parseConfig()
  } catch (error) {
    errorMessage.value = displayError(error)
    return
  }
  submitting.value = true
  try {
    result.value = await api.createGroupRevision(props.group.id, { engine: engine.value, config, idempotency_key: idempotencyKey.value })
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}

function finish(): void {
  if (result.value) emit('published', result.value.generation.generation)
  else emit('close')
}
</script>

<template>
  <BaseModal title="高级 · 发布原始配置" :description="`${group.name} · 仅供引擎集成调试`" width="large" :close-disabled="submitting" @close="finish">
    <form v-if="!result" id="publish-revision-form" class="form-stack" @submit.prevent="submit">
      <p class="form-note form-note--warning">当前 Agent 只校验并保存配置文件；发布成功不代表 Xray / GOST 已启动。日常线路请使用“转发规则”。</p>
      <div class="revision-summary">
        <span>当前组修订 <strong>{{ group.current_generation }}</strong></span>
        <span>计划发布 <strong>{{ nextRevision }}</strong></span>
        <span>成员 <strong>{{ group.member_count }}</strong></span>
      </div>
      <label class="field"><span>配置引擎</span><select v-model="engine"><option value="xray">Xray</option><option value="gost">GOST</option></select></label>
      <label class="field"><span>配置内容</span><textarea v-model="configText" class="code-editor" rows="14" spellcheck="false" /></label>
      <p v-if="group.member_count === 0" class="form-note form-note--warning">该设备组没有成员。修订会保存，但不会产生节点配置分配。</p>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </form>

    <div v-else class="publish-result">
      <CheckCircle2 :size="30" />
      <h3>组修订 {{ result.generation.generation }} 已发布</h3>
      <p>已为 {{ result.assignments.length }} 个节点生成完整配置。</p>
      <dl>
        <div><dt>引擎</dt><dd>{{ result.generation.engine }}</dd></div>
        <div><dt>配置哈希</dt><dd><code>{{ result.generation.config_hash }}</code></dd></div>
        <div><dt>请求结果</dt><dd>{{ result.replayed ? '幂等重放' : '新建修订' }}</dd></div>
      </dl>
    </div>

    <template #footer>
      <button v-if="!result" class="button button--secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button v-if="!result" class="button button--primary" type="submit" form="publish-revision-form" :disabled="submitting"><LoaderCircle v-if="submitting" class="spin" :size="16" /><Send v-else :size="16" />{{ submitting ? '正在发布' : '发布组修订' }}</button>
      <button v-else class="button button--primary" type="button" @click="finish">完成</button>
    </template>
  </BaseModal>
</template>
