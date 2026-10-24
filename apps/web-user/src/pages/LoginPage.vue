<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'

import { sharedLoginURL } from '@/lib/sharedLogin'

const route = useRoute()
onMounted(() => window.location.replace(sharedLoginURL(route.query)))
</script>

<template>
  <main class="login-page"><p role="status">正在前往登录页…</p></main>
</template>
