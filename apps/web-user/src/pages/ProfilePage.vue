<script setup lang="ts">
import { KeyRound, LoaderCircle, RefreshCw, X } from '@lucide/vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { customerApi, type CustomerProfile, type UsageInfo } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import StatusPill from '@/components/StatusPill.vue'
import { bytes, dateTime } from '@/lib/format'
import { customerAuth } from '@/stores/auth'

const profile = ref<CustomerProfile | null>(null)
const usage = ref<UsageInfo | null>(null)
const loading = ref(true)
const error = ref('')
const passwordOpen = ref(false)
const passwordBusy = ref(false)
const passwordError = ref('')
const passwordSuccess = ref('')
const form = reactive({ current: '', next: '', confirm: '' })
const usageText = computed(() => profile.value ? `${bytes(usage.value?.traffic_used_bytes ?? profile.value.traffic_used_bytes)} / ${profile.value.traffic_limit_bytes ? bytes(usage.value?.traffic_limit_bytes ?? profile.value.traffic_limit_bytes) : '不限'}` : '')

async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try {
    const [user, usageResult] = await Promise.all([customerApi.me(), customerApi.usage()])
    profile.value = user; usage.value = usageResult; customerAuth.updateUser(user)
  } catch (cause) { error.value = cause instanceof Error ? cause.message : '用户信息加载失败' }
  finally { loading.value = false }
}

function openPassword(): void {
  form.current = ''; form.next = ''; form.confirm = ''; passwordError.value = ''; passwordSuccess.value = ''; passwordOpen.value = true
}

async function changePassword(): Promise<void> {
  if (passwordBusy.value) return
  passwordError.value = ''; passwordSuccess.value = ''
  if (!form.current || !form.next) { passwordError.value = '当前密码和新密码不能为空'; return }
  if (form.next !== form.confirm) { passwordError.value = '两次输入的新密码不一致'; return }
  passwordBusy.value = true
  try { await customerApi.changePassword(form.current, form.next); passwordSuccess.value = '密码已修改'; form.current = ''; form.next = ''; form.confirm = '' }
  catch (cause) { passwordError.value = cause instanceof Error ? cause.message : '密码修改失败' }
  finally { passwordBusy.value = false }
}
onMounted(load)
</script>

<template>
  <div class="page-stack profile-page">
    <StatePanel v-if="loading && !profile" state="loading" title="正在读取用户信息" />
    <StatePanel v-else-if="error && !profile" state="error" title="用户信息加载失败" :message="error" @retry="load" />
    <section v-else-if="profile" class="profile-section">
      <header><h1>用户信息</h1><div class="profile-actions"><StatusPill :status="profile.effective_status" /><button class="icon-button" type="button" title="刷新" aria-label="刷新用户信息" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" /></button></div></header>
      <dl class="profile-grid">
        <div><dt>UID</dt><dd>{{ profile.id }}</dd></div>
        <div><dt>用户名</dt><dd>{{ profile.username }}</dd></div>
        <div><dt>用户类型</dt><dd>{{ profile.user_type === 'administrator' ? '管理员' : '普通用户' }}</dd></div>
        <div><dt>用户组</dt><dd>{{ profile.user_group_name || '未分组' }}</dd></div>
        <div><dt>到期时间</dt><dd>{{ dateTime(profile.expires_at) }}</dd></div>
        <div><dt>流量</dt><dd>{{ usageText }}</dd></div>
        <div><dt>最大规则数</dt><dd>{{ profile.max_rules || '不限' }}</dd></div>
        <div><dt>速率限制</dt><dd>{{ profile.speed_limit_mbps ? `${profile.speed_limit_mbps} Mbps` : '不限' }}</dd></div>
        <div><dt>IP 数限制</dt><dd>{{ profile.ip_limit || '不限' }}</dd></div>
        <div><dt>连接数限制</dt><dd>{{ profile.connection_limit || '不限' }}</dd></div>
      </dl>
      <footer><button class="button button-secondary" @click="openPassword"><KeyRound :size="15" />修改密码</button></footer>
    </section>
    <p v-if="error && profile" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>

    <div v-if="passwordOpen" class="modal-backdrop" @click.self="passwordOpen = false">
      <section class="modal" role="dialog" aria-modal="true" aria-labelledby="password-title">
        <header><h2 id="password-title">修改密码</h2><button class="icon-button" aria-label="关闭" :disabled="passwordBusy" @click="passwordOpen = false"><X :size="17" /></button></header>
        <form id="password-form" @submit.prevent="changePassword">
          <label><span>当前密码</span><input v-model="form.current" type="password" autocomplete="current-password" maxlength="4096" required /></label>
          <label><span>新密码</span><input v-model="form.next" type="password" autocomplete="new-password" maxlength="4096" required /></label>
          <label><span>确认新密码</span><input v-model="form.confirm" type="password" autocomplete="new-password" maxlength="4096" required /></label>
          <p v-if="passwordError" class="form-error" role="alert">{{ passwordError }}</p><p v-if="passwordSuccess" class="form-success" role="status">{{ passwordSuccess }}</p>
        </form>
        <footer><button class="button button-secondary" :disabled="passwordBusy" @click="passwordOpen = false">取消</button><button class="button button-primary" type="submit" form="password-form" :disabled="passwordBusy"><LoaderCircle v-if="passwordBusy" class="spin" :size="15" />确定</button></footer>
      </section>
    </div>
  </div>
</template>
