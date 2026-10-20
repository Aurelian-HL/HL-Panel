<script setup lang="ts">
import ToastRegion from '@/components/ToastRegion.vue'
import PanelLicenseReminder from '@/components/PanelLicenseReminder.vue'
</script>

<template>
  <RouterView />
  <ToastRegion />
  <PanelLicenseReminder />
</template>
