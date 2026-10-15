<script setup lang="ts">
import { ref } from 'vue'
import { X } from '@lucide/vue'
import { useDialogFocus } from '@/composables/useDialogFocus'

const props = withDefaults(defineProps<{
  title: string
  description?: string
  width?: 'small' | 'medium' | 'large'
  closeDisabled?: boolean
  dialogClass?: string
}>(), { description: '', width: 'medium', closeDisabled: false })

const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLElement | null>(null)

function close(): void {
  if (!props.closeDisabled) emit('close')
}

useDialogFocus(dialog, close)
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" role="presentation" @mousedown.self="close">
      <section ref="dialog" tabindex="-1" class="modal" :class="[`modal--${width}`, dialogClass]" role="dialog" aria-modal="true" :aria-label="title">
        <header class="modal__header">
          <div>
            <h2>{{ title }}</h2>
            <p v-if="description">{{ description }}</p>
          </div>
          <button class="icon-button" type="button" aria-label="关闭对话框" title="关闭" :disabled="closeDisabled" @click="close">
            <X :size="19" />
          </button>
        </header>
        <div class="modal__body"><slot /></div>
        <footer v-if="$slots.footer" class="modal__footer"><slot name="footer" /></footer>
      </section>
    </div>
  </Teleport>
</template>
