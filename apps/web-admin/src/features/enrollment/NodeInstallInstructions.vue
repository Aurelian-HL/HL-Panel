<script setup lang="ts">
import { computed, ref } from 'vue'
import { Clipboard } from '@lucide/vue'
import { toast } from '@/composables/toast'
import { nodeInstallCommand, nodeRepository } from './nodeInstall'

const props = defineProps<{ offline?: boolean; token: string }>()
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
    <h3>{{ offline ? '离线安装节点' : '一条命令安装并注册节点' }}</h3>
    <template v-if="!offline">
      <p>通过 SSH 登录目标节点，使用 root 执行。命令已包含此令牌，自动安装 HL Agent、主机探针、Xray 和 GOST，引擎由 Agent 管理。支持 Linux amd64 / systemd，需预装 curl。</p>
      <p v-if="instruction.error" class="form-error" role="alert">{{ instruction.error }}</p>
      <template v-else>
        <pre data-testid="node-install-command"><code>{{ instruction.command }}</code></pre>
        <button class="button button--secondary" type="button" @click="copyCommand"><Clipboard :size="15" />{{ copied ? '安装命令已复制' : '复制安装命令' }}</button>
      </template>
    </template>
    <template v-else>
      <p>从正式发布页下载同一版本的安装包和 .sha256 文件，在可联网的机器解压。将安装包、摘要文件和 deploy/install-node.sh 上传到节点同一目录。</p>
      <pre><code>bash install-node.sh --version v版本号 --panel-url https://你的面板域名 --token '{{ token }}' --archive ./hl-panel-linux-amd64.tar.gz --sha256 ./hl-panel-linux-amd64.tar.gz.sha256</code></pre>
      <p>将版本号与面板地址替换为实际值。离线指无需连接 GitHub；节点仍须连接面板，并预装 python3、CA 证书及 systemd。</p>
    </template>
    <p>无需再次输入令牌。令牌仅限这一台节点使用，过期或已用需重新生成；命令含一次性秘密，请勿公开分享，执行后清除对应命令历史。</p>
    <h3>返回面板确认在线</h3>
    <p>安装会注册 HL Agent、启用开机自启，并上报 CPU、主机内存、根磁盘、网络和连接数。约 30 秒后刷新设备组和探针；CPU 与速率从第二次采样开始显示。</p>
    <p>引擎已配置为 mixed 并允许自动启动；面板下发有效转发规则后启动相应进程。安装不会自行创建业务规则或开通专线。已有哪吒无需重装，关联是可选项。</p>
    <details><summary>安装失败或没有上线</summary><p>下载和短暂连接失败会自动重试，注册最多等待 90 秒。失败后保留安装与恢复状态，检查网络和 HTTPS 证书后重跑同一命令；仅令牌过期或失效时重新生成。安装版本默认跟随当前面板，已注册机器保留原身份。</p><pre><code>systemctl status hl-panel-edge-agent --no-pager
journalctl -u hl-panel-edge-agent -n 50 --no-pager</code></pre><p>纯 IP 自签名证书需先安装可信 CA，不能关闭证书校验。不要分享包含注册凭据的文件。</p></details>
    <a :href="`${nodeRepository}/releases/latest`" target="_blank" rel="noopener noreferrer">查看官方安装包与版本</a>
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
