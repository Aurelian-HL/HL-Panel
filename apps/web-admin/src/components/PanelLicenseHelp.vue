<script setup lang="ts">
import { ref } from 'vue'
import { LifeBuoy } from '@lucide/vue'

const expanded = ref(false)
</script>

<template>
  <div class="license-help">
    <button type="button" class="button button--secondary" :aria-expanded="expanded" @click="expanded = !expanded"><LifeBuoy :size="16" />寻求帮助</button>
    <p v-if="expanded" class="license-help-placeholder" role="status">联系方式待补充。</p>
  </div>
</template>

<style scoped>
.license-help { display: grid; gap: 8px; min-width: 0; }
.license-help .button { justify-content: center; gap: 7px; }
.license-help-placeholder { margin: 0; color: #65748a; font-size: 13px; line-height: 1.6; }
</style>
