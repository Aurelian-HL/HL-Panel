import { createRouter, createWebHistory } from 'vue-router'

import UserShell from '@/layouts/UserShell.vue'
import { customerAuth } from '@/stores/auth'

const LoginPage = () => import('@/pages/LoginPage.vue')
const HomePage = () => import('@/pages/HomePage.vue')
const ProfilePage = () => import('@/pages/ProfilePage.vue')
const ServicesPage = () => import('@/pages/ServicesPage.vue')

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
    {
      path: '/', component: UserShell, children: [
        { path: '', name: 'home', component: HomePage },
        { path: 'userinfo', name: 'userinfo', component: ProfilePage },
        { path: 'forward_rules', alias: 'forward-rules', name: 'forward-rules', component: ServicesPage },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
  if (!to.meta.public && !customerAuth.isAuthenticated.value) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.name === 'login' && customerAuth.isAuthenticated.value) return { name: 'home' }
  return true
})
