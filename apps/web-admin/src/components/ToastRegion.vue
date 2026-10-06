<script setup lang="ts">
import { AlertCircle, CheckCircle2, Info, X } from '@lucide/vue'

import { toast } from '@/composables/toast'

const icons = { success: CheckCircle2, error: AlertCircle, info: Info }
</script>

<template>
  <div class="toast-region" aria-live="polite" aria-atomic="false">
    <TransitionGroup name="toast">
      <article v-for="item in toast.messages.value" :key="item.id" class="toast" :class="`toast--${item.type}`">
        <component :is="icons[item.type]" :size="19" aria-hidden="true" />
        <div class="toast__content">
          <strong>{{ item.title }}</strong>
          <span v-if="item.message">{{ item.message }}</span>
        </div>
        <button class="icon-button icon-button--compact" type="button" aria-label="关闭提示" title="关闭" @click="toast.remove(item.id)">
          <X :size="16" />
        </button>
      </article>
    </TransitionGroup>
  </div>
</template>
