<script setup lang="ts">
import { computed, ref } from 'vue'
import { Clipboard } from '@lucide/vue'
import { toast } from '@/composables/toast'
import { nodeInstallCommand, nodeRepository } from './nodeInstall'

const props = defineProps<{ token: string }>()
const copied = ref(false)
const instruction = computed(() => {
  try { return { command: nodeInstallCommand(window.location.origin, props.token), error: '' } }
  catch (cause) { return { command: '', error: cause instanceof Error ? cause.message : '无法生成安装命令' } }
})
async function copyCommand() {
  try {
    await navigator.clipboard.writeText(instruction.value.command)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1800)
  } catch { toast.error('复制失败', '请手动选择安装命令复制') }
}
</script>

<template>
  <section class="node-install" aria-label="节点安装步骤">
    <h3>复制命令，在节点服务器执行</h3>
    <p>使用 root 登录 Linux amd64 / systemd 服务器，粘贴执行即可。自动补齐依赖、安装转发与探针，并加入当前设备组。</p>
    <p v-if="instruction.error" class="form-error" role="alert">{{ instruction.error }}</p>
    <template v-else>
      <pre data-testid="node-install-command"><code>{{ instruction.command }}</code></pre>
      <button class="button button--primary" type="button" @click="copyCommand"><Clipboard :size="15" />{{ copied ? '已复制' : '复制安装命令' }}</button>
    </template>
    <p>已安装的依赖自动跳过；BBR 按内核能力启用，安装成功后自动清理临时文件。</p>
    <p>一台机器一条命令，请勿分享。重跑保留节点身份和累计流量；换组或重装会短暂中断该节点连接。</p>
    <details>
      <summary>安装失败或未上线</summary>
      <p>检查服务器网络及面板 HTTPS 证书后重跑本次命令；令牌过期或失效时重新生成。默认安装最新正式版，完成后返回面板查看在线状态。</p>
      <pre><code>systemctl status hl-panel-edge-agent --no-pager
journalctl -u hl-panel-edge-agent -n 30 --no-pager</code></pre>
      <a :href="`${nodeRepository}/releases/latest`" target="_blank" rel="noopener noreferrer">查看正式版本</a>
    </details>
  </section>
</template>

<style scoped>
.node-install { display: grid; gap: 10px; min-width: 0; padding: 14px; border: 1px solid var(--gray-200); background: var(--gray-50); }
h3 { margin: 0; color: var(--navy-900); font-size: 13px; }
p { margin: 0; font-size: 12px; line-height: 1.7; color: var(--gray-600); }
pre { margin: 0; padding: 12px; min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; background: var(--navy-900); color: #fff; font-size: 11px; line-height: 1.7; }
button { justify-self: start; }
details { font-size: 12px; line-height: 1.7; }
details p, details pre { margin-top: 8px; }
summary { cursor: pointer; }
a { font-size: 12px; color: var(--navy-800); }
@media (max-width: 600px) { .node-install { padding: 10px; } button { width: 100%; justify-content: center; } }
</style>
