<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity, ChevronDown, Home, LogOut, Menu, Network, ServerCog, Settings, User, Users, UsersRound, X } from '@lucide/vue'
import { authStore } from '@/stores/auth'
import { siteStore } from '@/stores/site'

const route = useRoute()
const router = useRouter()
const mobileNavigationOpen = ref(false)
const managementOpen = ref(true)
watch(() => route.fullPath, () => { mobileNavigationOpen.value = false })
onMounted(() => { void siteStore.load().catch(() => undefined) })
watch(() => siteStore.panelTitle.value, (title) => {
  document.title = title
}, { immediate: true })
const general = [
  { label: '主页', icon: Home, path: '/overview' },
  { label: '个人中心', icon: User, path: '/userinfo' },
  { label: '转发规则', icon: Network, path: '/forward-rules' },
  { label: '设备探针', icon: Activity, path: '/probe' },
]
const management = [
  { label: '用户管理', icon: Users, path: '/customers' },
  { label: '用户组管理', icon: UsersRound, path: '/user-groups' },
  { label: '设备组管理', icon: ServerCog, path: '/device-groups' },
  { label: '系统设置', icon: Settings, path: '/system-settings' },
]
async function logout(): Promise<void> { authStore.logout(); await router.replace('/login') }
function onNavigationClick(path: string): void {
  if (path === '/probe') mobileNavigationOpen.value = false
}
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="topbar__brand">
        <button class="icon-button mobile-nav-toggle" type="button" :aria-expanded="mobileNavigationOpen" aria-controls="panel-navigation" aria-label="展开菜单" @click="mobileNavigationOpen = !mobileNavigationOpen"><X v-if="mobileNavigationOpen" :size="18" /><Menu v-else :size="18" /></button>
        <RouterLink to="/overview" class="brand" aria-label="HL-panel 主页">{{ siteStore.siteName.value }}</RouterLink>
      </div>
      <div class="topbar__actions">
        <RouterLink to="/userinfo" class="topbar__user" title="个人中心">{{ authStore.user.value?.username }}<User :size="17" /></RouterLink>
        <button class="icon-button" type="button" aria-label="退出登录" title="退出登录" @click="logout"><LogOut :size="17" /></button>
      </div>
    </header>
    <div v-if="mobileNavigationOpen" class="mobile-navigation-backdrop" @click="mobileNavigationOpen = false" />
    <aside id="panel-navigation" class="sidebar" :class="{ 'sidebar--open': mobileNavigationOpen }">
      <nav class="primary-nav" aria-label="主导航">
        <RouterLink v-for="item in general" :key="item.path" :to="item.path" :target="item.path === '/probe' ? '_blank' : undefined" :rel="item.path === '/probe' ? 'opener' : undefined" @click="onNavigationClick(item.path)"><component :is="item.icon" :size="16" /><span>{{ item.label }}</span></RouterLink>
        <button class="nav-section-toggle" :aria-expanded="managementOpen" @click="managementOpen = !managementOpen">管理<ChevronDown :size="14" :class="{ 'nav-collapsed': !managementOpen }" /></button>
        <div v-show="managementOpen" class="nav-management"><RouterLink v-for="item in management" :key="item.path" :to="item.path"><component :is="item.icon" :size="16" /><span>{{ item.label }}</span></RouterLink></div>
      </nav>
    </aside>
    <section class="workspace"><main class="page-content"><RouterView /></main></section>
  </div>
</template>
