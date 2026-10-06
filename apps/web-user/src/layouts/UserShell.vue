<script setup lang="ts">
import { ChevronDown, Home, LogOut, Menu, Moon, Network, Sun, UserRound, X } from '@lucide/vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { customerAuth } from '@/stores/auth'
import { customerSite } from '@/stores/site'

const route = useRoute()
const router = useRouter()
const navigationOpen = ref(false)
const accountOpen = ref(false)
const dark = ref(sessionStorage.getItem('ny_customer_theme') === 'dark')
const siteName = computed(() => customerSite.info.value?.site_name || 'HL-panel')
const items = [
  { label: '主页', path: '/', icon: Home },
  { label: '个人中心', path: '/userinfo', icon: UserRound },
  { label: '转发规则', path: '/forward_rules', icon: Network },
]
watch(() => route.fullPath, () => { navigationOpen.value = false; accountOpen.value = false })
watch(dark, (value) => {
  document.documentElement.dataset.theme = value ? 'dark' : 'light'
  sessionStorage.setItem('ny_customer_theme', value ? 'dark' : 'light')
}, { immediate: true })
onMounted(() => { void customerSite.load().catch(() => undefined) })
async function logout(): Promise<void> { await customerAuth.logout(); await router.replace('/login') }
</script>

<template>
  <div class="user-shell" :class="{ 'user-shell--dark': dark }">
    <header class="user-topbar">
      <div class="user-brand">
        <button class="header-icon nav-toggle" type="button" :aria-expanded="navigationOpen" aria-controls="customer-navigation" aria-label="展开菜单" @click="navigationOpen = !navigationOpen"><X v-if="navigationOpen" :size="20" /><Menu v-else :size="20" /></button>
        <RouterLink to="/" :title="siteName">{{ siteName }}</RouterLink>
      </div>
      <div class="account-actions">
        <div class="account-menu">
          <button class="account-trigger" type="button" aria-label="用户菜单" :aria-expanded="accountOpen" @click="accountOpen = !accountOpen"><UserRound :size="16" /><span>{{ customerAuth.user.value?.username }}</span><ChevronDown :size="14" /></button>
          <div v-if="accountOpen" class="account-dropdown"><button type="button" @click="logout"><LogOut :size="15" />退出登录</button></div>
        </div>
        <button class="header-icon" type="button" :title="dark ? '切换浅色模式' : '切换深色模式'" :aria-label="dark ? '切换浅色模式' : '切换深色模式'" @click="dark = !dark"><Sun v-if="dark" :size="18" /><Moon v-else :size="18" /></button>
      </div>
    </header>
    <div v-if="navigationOpen" class="nav-backdrop" @click="navigationOpen = false" />
    <div class="user-content">
      <aside id="customer-navigation" class="user-sidebar" :class="{ open: navigationOpen }"><nav aria-label="用户导航"><RouterLink v-for="item in items" :key="item.path" :to="item.path"><component :is="item.icon" :size="16" /><span>{{ item.label }}</span></RouterLink></nav></aside>
      <main class="user-workspace"><RouterView /></main>
    </div>
  </div>
</template>
