<script setup lang="ts">
import { computed } from 'vue'

import type { EdgeNode } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { engineSummary, formatDateTime, formatRelativeTime } from '@/lib/displayFormatters'
import { formatBytes, hasFreshTelemetry, resourceInteger } from './nodeTelemetry'

const props = defineProps<{ node: EdgeNode }>()
defineEmits<{ close: [] }>()

const resourceRows = computed(() => {
  const rows: Array<{ label: string; value: string }> = []
  const cpus = resourceInteger(props.node, 'logical_cpus')
  const maxProcs = resourceInteger(props.node, 'go_max_procs')
  const allocated = resourceInteger(props.node, 'memory_alloc_bytes')
  const system = resourceInteger(props.node, 'memory_system_bytes')
  if (cpus !== null) rows.push({ label: '逻辑 CPU', value: `${cpus}` })
  if (maxProcs !== null) rows.push({ label: 'Go 并行度', value: `${maxProcs}` })
  if (allocated !== null) rows.push({ label: 'Agent 已分配内存', value: formatBytes(allocated) })
  if (system !== null) rows.push({ label: 'Agent Go 运行时内存', value: formatBytes(system) })
  return rows
})
const freshTelemetry = computed(() => hasFreshTelemetry(props.node))
</script>

<template>
  <BaseModal :title="node.name || node.hostname" description="节点状态与最近一次上报" width="medium" @close="$emit('close')">
    <div class="node-details">
      <div class="node-details__status"><StatusBadge :status="node.status" /><span>{{ node.hostname || '主机名未上报' }}</span></div>

      <section aria-label="配置同步">
        <h3>配置同步</h3>
        <dl class="node-details__grid">
          <div><dt>已应用 / 期望代数</dt><dd>{{ node.applied_generation }} / {{ node.desired_generation }}</dd></div>
          <div><dt>最近应用结果</dt><dd>{{ node.last_apply_status || '未上报' }}</dd></div>
          <div><dt>最近心跳</dt><dd>{{ formatRelativeTime(node.last_heartbeat_at) }} <small>{{ formatDateTime(node.last_heartbeat_at) }}</small></dd></div>
          <div><dt>首次注册</dt><dd>{{ formatDateTime(node.created_at) }}</dd></div>
        </dl>
        <p v-if="node.last_apply_message" class="inline-warning" role="alert">{{ node.last_apply_message }}</p>
        <p v-else-if="node.status === 'failed'" class="inline-warning" role="alert">最近一次配置应用失败；Agent 未上报具体错误，请检查节点侧日志。</p>
      </section>

      <section aria-label="运行环境">
        <h3>运行环境</h3>
        <dl class="node-details__grid">
          <div><dt>系统 / 架构</dt><dd>{{ node.platform || '未上报' }} / {{ node.architecture || '未上报' }}</dd></div>
          <div><dt>Agent 版本</dt><dd>{{ node.agent_version || '未上报' }}</dd></div>
          <div><dt>引擎版本</dt><dd>{{ engineSummary(node.engine_versions) }}</dd></div>
          <div><dt>能力</dt><dd>{{ node.capabilities.length ? node.capabilities.join('、') : '未上报' }}</dd></div>
          <div><dt>节点 ID</dt><dd class="node-details__identifier">{{ node.id }}</dd></div>
          <div><dt>启动 ID</dt><dd class="node-details__identifier">{{ node.boot_id || '未上报' }}</dd></div>
        </dl>
      </section>

      <section aria-label="Agent 采样">
        <h3>Agent 采样</h3>
        <p class="node-details__sample-state" :class="{ 'node-details__sample-state--stale': !freshTelemetry }">{{ !node.last_heartbeat_at ? '尚无采样' : freshTelemetry ? '最近心跳采样' : '心跳已过期或节点离线 · 以下仅为历史采样' }} · {{ formatDateTime(node.last_heartbeat_at) }}</p>
        <dl v-if="resourceRows.length" class="node-details__grid">
          <div v-for="row in resourceRows" :key="row.label"><dt>{{ row.label }}</dt><dd>{{ row.value }}</dd></div>
        </dl>
        <p v-else class="node-details__empty">Agent 未上报可展示的资源数据。</p>
        <p class="node-details__note">内存仅为 Agent Go 进程数据，不代表整机内存占用。</p>
      </section>

      <section aria-label="整机监测">
        <h3>整机监测</h3>
        <p class="node-details__empty">CPU 使用率、整机内存、磁盘、网络速率和连接数尚未接入。</p>
      </section>
    </div>
  </BaseModal>
</template>
