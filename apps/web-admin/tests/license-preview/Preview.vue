<script setup lang="ts">
import {ref} from 'vue'
import Card from '../../src/components/PanelLicenseCard.vue'
import Reminder from '../../src/components/PanelLicenseReminder.vue'
import {authStore} from '../../src/stores/auth'
import {panelLicenseStore} from '../../src/stores/panelLicense'
const scenario=ref('active')
async function login(){await authStore.login({username:'preview',password:'isolated-fixture-only'})}
async function change(){await fetch('/api/preview/scenario',{method:'POST',headers:{Authorization:'Bearer isolated-preview-session'},body:JSON.stringify({scenario:scenario.value})});await panelLicenseStore.refresh()}
</script>
<template><main class="preview"><header><small>仅限 127.0.0.1 · 合成数据 · 不连接生产</small><h1>面板域名授权验收</h1><p>使用正式授权组件，验收到期提醒、DNS 重试和多视口布局。</p></header><button v-if="!authStore.isAuthenticated.value" class="button button--primary" @click="login">登录隔离验收账号</button><template v-else><label class="field scenario"><span>验收状态</span><select v-model="scenario" @change="change"><option value="active">有效授权</option><option value="near">到期前 3 天</option><option value="expired">已到期</option><option value="renewed">续期恢复</option><option value="perpetual">永久授权</option><option value="unactivated">待激活</option><option value="unconfigured">配置待接入</option><option value="failure">提醒保存与同步失败</option><option value="sync_failure">同步限流失败</option></select></label><Card/><Reminder/></template></main></template>
<style>.preview{max-width:1040px;margin:0 auto;padding:48px 24px;display:grid;gap:24px}.preview h1{margin:12px 0}.preview p{line-height:1.7}.preview header small{color:#896b3a}.preview .scenario{max-width:360px}@media(max-width:600px){.preview{padding:24px 14px}.preview h1{font-size:24px}}</style>
