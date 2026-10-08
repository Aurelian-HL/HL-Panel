<script setup lang="ts">
import type { Subscription } from '@/api/subscriptions'
import SubscriptionActions from './SubscriptionActions.vue'
defineProps<{ items: Subscription[]; customerNames: Map<string, string>; busy: boolean }>()
defineEmits<{ inspect: [item: Subscription, edit: boolean]; publish: [item: Subscription]; download: [item: Subscription]; toggle: [item: Subscription] }>()
const status = (item: Subscription) => item.state === 'revoked' ? '已停用' : !item.published_revision ? '未发布' : item.pending_update ? '待更新' : '已发布'
const updated = (item: Subscription) => new Date(item.updated_at).toLocaleString()
</script>

<template>
  <div class="business-inventory subscription-inventory">
    <div class="business-desktop table-wrap"><table class="data-table business-table subscription-table">
      <thead><tr><th>名称 / 客户</th><th>线路</th><th>发布状态</th><th>更新时间</th><th>操作</th></tr></thead>
      <tbody><tr v-for="item in items" :key="item.id">
        <td><strong class="table-primary">{{ item.name }}</strong><small class="table-secondary">{{ customerNames.get(item.customer_id) || item.customer_id }}</small></td>
        <td>已发布 {{ item.published_line_count }} / 草稿 {{ item.line_count }}</td>
        <td><span class="business-status" :class="{ 'business-status--muted': item.state !== 'active' }">{{ status(item) }}</span></td>
        <td>{{ updated(item) }}</td>
        <td><SubscriptionActions :item="item" :busy="busy" @inspect="$emit('inspect', item, false)" @edit="$emit('inspect', item, true)" @publish="$emit('publish', item)" @download="$emit('download', item)" @toggle="$emit('toggle', item)" /></td>
      </tr></tbody>
    </table></div>
    <div class="business-mobile" aria-label="订阅卡片"><article v-for="item in items" :key="item.id" class="business-card subscription-card">
      <header><div><h3>{{ item.name }}</h3><p>{{ customerNames.get(item.customer_id) || item.customer_id }}</p></div><span class="business-status" :class="{ 'business-status--muted': item.state !== 'active' }">{{ status(item) }}</span></header>
      <dl><div><dt>已发布线路</dt><dd>{{ item.published_line_count }}</dd></div><div><dt>草稿线路</dt><dd>{{ item.line_count }}</dd></div><div class="subscription-card__updated"><dt>更新时间</dt><dd>{{ updated(item) }}</dd></div></dl>
      <footer><SubscriptionActions :item="item" :busy="busy" @inspect="$emit('inspect', item, false)" @edit="$emit('inspect', item, true)" @publish="$emit('publish', item)" @download="$emit('download', item)" @toggle="$emit('toggle', item)" /></footer>
    </article></div>
  </div>
</template>

<style scoped>
.subscription-inventory { container-type: inline-size; }
.subscription-table { min-width: 1000px; }
.subscription-table th:first-child { width: 23%; }
.subscription-table th:nth-child(2) { width: 14%; }
.subscription-table th:nth-child(3) { width: 10%; }
.subscription-table th:nth-child(4) { width: 18%; }
.subscription-table th:last-child { width: 35%; }
.subscription-card { min-width: 0; }
.subscription-card header > div { min-width: 0; }
.subscription-card h3, .subscription-card p, .subscription-card dd { overflow-wrap: anywhere; }
.subscription-card__updated { grid-column: 1 / -1; }
@container (max-width: 999px) {
  .business-desktop { display: none; }
  .business-mobile { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 330px), 1fr)); gap: 12px; }
}
</style>
