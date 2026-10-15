<script setup lang="ts">
import { ref } from 'vue'
import { useDialogFocus } from '@/composables/useDialogFocus'

const props = withDefaults(defineProps<{ closeDisabled?: boolean }>(), { closeDisabled: false })
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLElement | null>(null)
useDialogFocus(dialog, () => { if (!props.closeDisabled) emit('close') })
</script>

<template>
  <section ref="dialog" tabindex="-1" role="dialog" aria-modal="true"><slot /></section>
</template>
