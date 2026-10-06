<script setup lang="ts">
import { Pencil } from '@lucide/vue'
import type { Customer, UserGroup } from '@/api/business'
import BusinessStatus from '@/components/BusinessStatus.vue'
import { byteAmount } from '@/lib/businessFormatters'
import { formatDateTime } from '@/lib/displayFormatters'
const props = defineProps<{ customers: Customer[]; groups: UserGroup[] }>()
defineEmits<{ edit: [customer: Customer] }>()
const groupName = (id: string) => !id ? '未分组' : props.groups.find((item) => item.id === id)?.name ?? '用户组不可用'
</script>
<template>
  <div class="business-inventory">
    <div class="business-desktop table-wrap"><table class="data-table business-table customer-table">
      <thead><tr><th>用户名</th><th>过期时间</th><th>流量</th><th>用户组</th><th>最大规则数</th><th>状态</th><th>操作</th></tr></thead>
      <tbody><tr v-for="customer in customers" :key="customer.id">
        <td><strong class="table-primary">{{ customer.username }}</strong><small v-if="customer.display_name && customer.display_name !== customer.username" class="table-secondary">{{ customer.display_name }}</small></td>
        <td class="business-date">{{ customer.expires_at ? formatDateTime(customer.expires_at) : '永久' }}</td>
        <td><span class="table-primary">{{ byteAmount(customer.traffic_used_bytes) }} / {{ customer.traffic_limit_bytes ? byteAmount(customer.traffic_limit_bytes) : '不限' }}</span><small class="table-secondary">记录用量 / 额度</small></td>
        <td>{{ groupName(customer.user_group_id) }}</td>
        <td>{{ customer.max_rules || '不限' }}</td>
        <td><BusinessStatus :status="customer.effective_status" /></td>
        <td><button class="button button--quiet" @click="$emit('edit', customer)"><Pencil :size="14" />编辑</button></td>
      </tr></tbody>
    </table></div>
    <div class="business-mobile"><article v-for="customer in customers" :key="customer.id" class="business-card">
      <header><div><h3>{{ customer.username }}</h3><p>{{ groupName(customer.user_group_id) }}<span v-if="customer.display_name && customer.display_name !== customer.username"> · {{ customer.display_name }}</span></p></div><BusinessStatus :status="customer.effective_status" /></header>
      <dl><div><dt>过期时间</dt><dd>{{ customer.expires_at ? formatDateTime(customer.expires_at) : '永久' }}</dd></div><div><dt>最大规则数</dt><dd>{{ customer.max_rules || '不限' }}</dd></div><div><dt>记录流量</dt><dd>{{ byteAmount(customer.traffic_used_bytes) }}</dd></div><div><dt>流量额度</dt><dd>{{ customer.traffic_limit_bytes ? byteAmount(customer.traffic_limit_bytes) : '不限' }}</dd></div></dl>
      <footer><button class="button button--secondary" @click="$emit('edit', customer)"><Pencil :size="15" />编辑用户</button></footer>
    </article></div>
  </div>
</template>
