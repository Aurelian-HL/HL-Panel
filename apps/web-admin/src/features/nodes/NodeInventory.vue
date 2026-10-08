<script setup lang="ts">
import { Cpu, Eye, Server, Trash2 } from '@lucide/vue'

import type { EdgeNode } from '@/api'
import StatusBadge from '@/components/StatusBadge.vue'
import { engineSummary, formatDateTime, formatRelativeTime } from '@/lib/displayFormatters'
import { formatResourceBytes, hasFreshTelemetry } from './nodeTelemetry'

defineProps<{ nodes: EdgeNode[] }>()
defineEmits<{ inspect: [node: EdgeNode]; deleteNode: [node: EdgeNode] }>()
</script>

<template>
  <div class="inventory">
    <div class="table-wrap node-table-wrap">
      <table class="data-table node-table">
        <thead>
          <tr><th>节点</th><th>状态</th><th>运行环境</th><th>Agent 已分配内存</th><th>节点配置代数</th><th>最近心跳</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.id">
            <td>
              <div class="resource-name"><span><Server :size="17" /></span><div><strong>{{ node.name || node.hostname }}</strong><small>{{ node.hostname }}</small></div></div>
            </td>
            <td><StatusBadge :status="node.status" /></td>
            <td><strong class="table-primary">{{ node.platform || '未知系统' }} / {{ node.architecture || '未知架构' }}</strong><small class="table-secondary">Agent {{ node.agent_version || '未上报' }} · {{ engineSummary(node.engine_versions) }}</small></td>
            <td>
              <strong class="table-primary">{{ formatResourceBytes(node, 'memory_alloc_bytes') }}</strong>
              <small class="table-secondary" :class="{ 'text-warning': !hasFreshTelemetry(node) }">{{ !node.last_heartbeat_at ? '尚无采样' : hasFreshTelemetry(node) ? '最近心跳采样' : '历史采样 · 非实时' }}</small>
            </td>
            <td>
              <div class="generation-cell"><strong>{{ node.applied_generation }}</strong><span>/ {{ node.desired_generation }}</span></div>
              <small :class="node.applied_generation === node.desired_generation ? 'text-success' : 'text-warning'">{{ node.applied_generation === node.desired_generation ? '已同步' : '等待追平' }}</small>
            </td>
            <td><strong class="table-primary">{{ formatRelativeTime(node.last_heartbeat_at) }}</strong><small class="table-secondary">{{ formatDateTime(node.last_heartbeat_at) }}</small></td>
            <td><div class="node-row-actions"><button class="button button--secondary node-inspect" type="button" :aria-label="`查看${node.name || node.hostname}详情`" @click="$emit('inspect', node)"><Eye :size="15" />详情</button><button v-if="node.status === 'offline'" class="button button--quiet node-delete-button" type="button" :aria-label="'删除' + (node.name || node.hostname)" @click="$emit('deleteNode', node)"><Trash2 :size="15" />删除</button></div></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="mobile-resource-list">
      <article v-for="node in nodes" :key="node.id" class="mobile-resource-card node-mobile-card">
        <header>
          <div class="resource-name"><span><Server :size="17" /></span><div><strong>{{ node.name || node.hostname }}</strong><small>{{ node.hostname }}</small></div></div>
          <StatusBadge :status="node.status" />
        </header>
        <dl>
          <div><dt>节点配置代数</dt><dd>{{ node.applied_generation }} / {{ node.desired_generation }}</dd></div>
          <div><dt>最近心跳</dt><dd>{{ formatRelativeTime(node.last_heartbeat_at) }}</dd></div>
          <div><dt>Agent 已分配内存</dt><dd>{{ formatResourceBytes(node, 'memory_alloc_bytes') }}<small :class="{ 'text-warning': !hasFreshTelemetry(node) }">{{ !node.last_heartbeat_at ? '尚无采样' : hasFreshTelemetry(node) ? '最近心跳采样' : '历史采样 · 非实时' }}</small></dd></div>
          <div><dt><Cpu :size="14" /> 运行环境</dt><dd>{{ node.platform || '未知' }} / {{ node.architecture || '未知' }}</dd></div>
          <div><dt>引擎</dt><dd>{{ engineSummary(node.engine_versions) }}</dd></div>
        </dl>
        <footer><button class="button button--secondary node-inspect" type="button" :aria-label="`查看${node.name || node.hostname}详情`" @click="$emit('inspect', node)"><Eye :size="15" />详情</button><button v-if="node.status === 'offline'" class="button button--quiet node-delete-button" type="button" :aria-label="'删除' + (node.name || node.hostname)" @click="$emit('deleteNode', node)"><Trash2 :size="15" />删除</button></footer>
      </article>
    </div>
  </div>
</template>
