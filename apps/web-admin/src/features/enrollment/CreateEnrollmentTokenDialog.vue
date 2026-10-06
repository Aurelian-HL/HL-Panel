<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { Check, Clipboard, LoaderCircle } from '@lucide/vue'

import { api, type EnrollmentTokenResponse } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { toast } from '@/composables/toast'
import { displayError, formatDateTime } from '@/lib/displayFormatters'

const emit = defineEmits<{ close: [] }>()
const name = ref('')
const expiresInSeconds = ref(900)
const submitting = ref(false)
const errorMessage = ref('')
const issuedToken = ref<EnrollmentTokenResponse | null>(null)
const copied = ref(false)

const canSubmit = computed(() => name.value.trim().length >= 2 && name.value.trim().length <= 120)

async function createToken(): Promise<void> {
  if (submitting.value) return
  errorMessage.value = ''
  if (!canSubmit.value) {
    errorMessage.value = '令牌名称需要 2 至 120 个字符'
    return
  }
  submitting.value = true
  try {
    issuedToken.value = await api.createEnrollmentToken({ name: name.value.trim(), expires_in_seconds: expiresInSeconds.value })
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}

async function copyToken(): Promise<void> {
  if (!issuedToken.value) return
  try {
    await navigator.clipboard.writeText(issuedToken.value.token)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1800)
  } catch {
    toast.error('复制失败', '请手动选中令牌复制')
  }
}

function close(): void {
  issuedToken.value = null
  emit('close')
}

onBeforeUnmount(() => { issuedToken.value = null })
</script>

<template>
  <BaseModal title="注册节点" width="small" :close-disabled="submitting" @close="close">
    <form v-if="!issuedToken" id="enrollment-token-form" class="form-stack ny-compact-form" @submit.prevent="createToken">
      <label class="field">
        <span>节点标识</span>
        <input v-model="name" maxlength="120" placeholder="例如：gz-edge-03" autofocus />
      </label>
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack"><label class="field">
        <span>有效时间</span>
        <select v-model.number="expiresInSeconds">
          <option :value="900">15 分钟</option>
          <option :value="1800">30 分钟</option>
          <option :value="3600">1 小时</option>
        </select>
      </label></div></details>
      <small class="field-help">注册令牌仅显示一次，使用后失效。</small>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </form>

    <div v-else class="form-stack">
      <div><h3>注册令牌已生成</h3><p>{{ issuedToken.name }} · {{ formatDateTime(issuedToken.expires_at) }} 到期</p></div>
      <div class="secret-value">
        <code>{{ issuedToken.token }}</code>
        <button class="button button--secondary" type="button" @click="copyToken">
          <Check v-if="copied" :size="16" />
          <Clipboard v-else :size="16" />
          {{ copied ? '已复制' : '复制令牌' }}
        </button>
      </div>
      <p class="field-help">关闭后无法再次查看。请只发送给目标节点的安装人员。</p>
    </div>

    <template #footer>
      <button class="button button--secondary" type="button" :disabled="submitting" @click="close">{{ issuedToken ? '关闭' : '取消' }}</button>
      <button v-if="!issuedToken" class="button button--primary" type="submit" form="enrollment-token-form" :disabled="submitting || !canSubmit">
        <LoaderCircle v-if="submitting" class="spin" :size="14" />{{ submitting ? '生成中' : '确定' }}
      </button>
    </template>
  </BaseModal>
</template>
