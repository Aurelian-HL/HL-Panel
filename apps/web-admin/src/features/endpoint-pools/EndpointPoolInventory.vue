<script setup lang="ts">
import { Activity, Globe2, Network, Trash2, UsersRound } from '@lucide/vue'

import type { DeviceGroup, EndpointPool, LoadBalancingStrategy } from '@/api'
import { formatDateTime } from '@/lib/displayFormatters'

defineProps<{ pools: EndpointPool[]; groups: DeviceGroup[] }>()
defineEmits<{ delete: [pool: EndpointPool] }>()

const strategyLabels: Record<LoadBalancingStrategy, string> = {
  weighted_round_robin: '加权轮询',
  weighted_least_connections: '加权最少连接',
  rendezvous_hash: 'Rendezvous Hash',
}

// SOCKS5 is retained as a distinct legacy endpoint protocol. It must not be
// rendered as NY TCP, because ingress and landing protocols are separate.
const protocolLabels = { vless: 'VLESS + Reality + Vision', tcp: 'NY TCP', socks5: 'NY SOCKS5' } as const
</script>

<template>
  <div class="inventory">
    <div class="table-wrap endpoint-table-wrap">
      <table class="data-table endpoint-table">
        <thead>
          <tr><th>服务端点</th><th>设备组</th><th>协议</th><th>服务端调度</th><th>健康候选</th><th>最近更新</th></tr>
        </thead>
        <tbody>
          <tr v-for="pool in pools" :key="pool.id">
            <td>
              <div class="endpoint-primary-cell">
                <div class="resource-name">
                  <span><Globe2 :size="17" /></span>
                  <div>
                    <strong>{{ pool.name }}</strong>
                    <small class="endpoint-address">{{ pool.hostname }}:{{ pool.port }}</small>
                    <small class="endpoint-single-note">单服务端点 · 组成员仅在服务端调度</small>
                  </div>
                </div>
                <button class="icon-button icon-button--compact" type="button" :aria-label="`删除服务端点 ${pool.name}`" title="删除服务端点" @click="$emit('delete', pool)"><Trash2 :size="15" /></button>
              </div>
            </td>
            <td><span class="table-primary">{{ groups.find((group) => group.id === pool.group_id)?.name || pool.group_id }}</span></td>
            <td><span class="protocol-badge" :class="`protocol-badge--${pool.protocol}`">{{ protocolLabels[pool.protocol] }}</span></td>
            <td><span class="table-primary">{{ strategyLabels[pool.selection_policy] }}</span><small class="table-secondary">{{ pool.mode }}</small></td>
            <td><span class="health-count" :class="{ 'health-count--warning': pool.healthy_candidate_count === 0 }"><Activity :size="15" />{{ pool.healthy_candidate_count }}<small>/ {{ pool.member_count }}</small></span></td>
            <td><span class="table-primary">{{ formatDateTime(pool.updated_at) }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="mobile-resource-list">
      <article v-for="pool in pools" :key="pool.id" class="mobile-resource-card endpoint-mobile-card">
        <header>
          <div class="resource-name"><span><Globe2 :size="17" /></span><div><strong>{{ pool.name }}</strong><small>{{ pool.hostname }}:{{ pool.port }}</small></div></div>
          <span class="protocol-badge" :class="`protocol-badge--${pool.protocol}`">{{ protocolLabels[pool.protocol] }}</span>
        </header>
        <p class="single-endpoint-note"><Network :size="14" />组成员自动同步；健康候选以节点授权状态为准</p>
        <dl>
          <div><dt>设备组</dt><dd>{{ groups.find((group) => group.id === pool.group_id)?.name || pool.group_id }}</dd></div>
          <div><dt>健康候选</dt><dd class="health-count" :class="{ 'health-count--warning': pool.healthy_candidate_count === 0 }"><Activity :size="14" />{{ pool.healthy_candidate_count }} / {{ pool.member_count }}</dd></div>
          <div><dt>调度策略</dt><dd>{{ strategyLabels[pool.selection_policy] }}</dd></div>
          <div><dt>最近更新</dt><dd>{{ formatDateTime(pool.updated_at) }}</dd></div>
        </dl>
        <footer><span class="mobile-resource-hint"><UsersRound :size="14" />成员仅在服务端候选池内</span><button class="button button--danger" type="button" @click="$emit('delete', pool)"><Trash2 :size="14" />删除</button></footer>
      </article>
    </div>
  </div>
</template>

<style scoped>
.endpoint-primary-cell { display: flex; min-width: 260px; align-items: center; justify-content: space-between; gap: 12px; }
.endpoint-primary-cell .resource-name { min-width: 0; }
.endpoint-primary-cell .icon-button { flex: 0 0 auto; }
.endpoint-mobile-card footer { align-items: center; justify-content: space-between; }
</style>
