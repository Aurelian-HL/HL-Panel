import { createRouter, createWebHistory } from 'vue-router'

import AppShell from '@/layouts/AppShell.vue'
import { authStore } from '@/stores/auth'

const LoginPage = () => import('@/pages/LoginPage.vue')
const OverviewPage = () => import('@/pages/OverviewPage.vue')
const ProfilePage = () => import('@/pages/ProfilePage.vue')
const NodesPage = () => import('@/pages/NodesPage.vue')
const ProbePage = () => import('@/pages/ProbePage.vue')
const ProbeWorkspace = () => import('@/layouts/ProbeWorkspace.vue')
const DeviceGroupsPage = () => import('@/pages/DeviceGroupsPage.vue')
const CustomersPage = () => import('@/pages/CustomersPage.vue')
const UserGroupsPage = () => import('@/pages/UserGroupsPage.vue')
const ForwardRulesPage = () => import('@/pages/ForwardRulesPage.vue')
const RuleGroupsPage = () => import('@/pages/RuleGroupsPage.vue')
const TrafficPage = () => import('@/pages/TrafficPage.vue')
const SystemSettingsPage = () => import('@/pages/SystemSettingsPage.vue')
const LookingGlassPage = () => import('@/pages/LookingGlassPage.vue')

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
    {
      path: '/',
      component: AppShell,
      children: [
        { path: '', redirect: '/overview' },
        { path: 'overview', name: 'overview', component: OverviewPage },
        { path: 'userinfo', name: 'userinfo', component: ProfilePage },
        { path: 'forward-rules', name: 'forward-rules', component: ForwardRulesPage },
        { path: 'rule-groups', name: 'rule-groups', component: RuleGroupsPage },
        { path: 'traffic', name: 'traffic', component: TrafficPage },
        { path: 'system-settings', name: 'system-settings', component: SystemSettingsPage },
        { path: 'customers', name: 'customers', component: CustomersPage },
        { path: 'user-groups', name: 'user-groups', component: UserGroupsPage },
        { path: 'nodes', name: 'nodes', component: NodesPage },
        { path: 'lookingglass', name: 'lookingglass', component: LookingGlassPage },
        { path: 'device-groups', name: 'device-groups', component: DeviceGroupsPage },
        { path: 'endpoint-pools', redirect: '/forward-rules' },
      ],
    },
    { path: '/probe', component: ProbeWorkspace, children: [{ path: '', name: 'probe', component: ProbePage }] },
    { path: '/:pathMatch(.*)*', redirect: '/overview' },
  ],
})

router.beforeEach((to) => {
  if (!to.meta.public && !authStore.isAuthenticated.value) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.name === 'login' && authStore.isAuthenticated.value) {
    return { name: 'overview' }
  }
  return true
})
