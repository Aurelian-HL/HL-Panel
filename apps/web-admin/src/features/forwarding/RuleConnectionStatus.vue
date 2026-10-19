<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ForwardRule } from '@/api/business'
import { ruleConnectionPresentation, type RuleConnectionPresentation } from './ruleConnection'
const props = defineProps<{ rule: ForwardRule; elapsed: number; presentation?: RuleConnectionPresentation }>()
const expanded = ref(false)
const state = computed(() => props.presentation ?? ruleConnectionPresentation(props.rule, props.elapsed))
</script>
<template>
  <button type="button" class="rule-connection" :class="'is-' + state.status" :title="state.title + ' 点击查看采样说明'" :aria-label="rule.name + '：' + state.label + '，查看连接采样详情'" :aria-expanded="expanded" @click="expanded = !expanded">{{ state.label }}<span aria-hidden="true" class="connection-disclosure">{{ expanded ? '−' : '+' }}</span></button>
  <span v-if="expanded" class="rule-connection-detail" role="note"><span v-for="(line, index) in state.details" :key="index">{{ line }}</span></span>
</template>
<style scoped>
.rule-connection { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 4px; padding: 1px 6px; border: 0; border-radius: 4px; font: inherit; font-size: 11px; font-weight: 400; line-height: 18px; white-space: nowrap; cursor: pointer; }
.rule-connection::before { content: ''; width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.rule-connection:focus-visible { outline: 2px solid var(--navy-700); outline-offset: 2px; }
.rule-connection.is-online { color: #23834b; background: #eaf7ef; }
.rule-connection.is-offline { color: #747e8c; background: #f0f2f5; }
.rule-connection.is-error { color: #946500; background: #fff5d9; }
.connection-disclosure { opacity: .7; }
.rule-connection-detail { display: flex; flex: 1 1 100%; min-width: 0; flex-direction: column; gap: 4px; padding: 7px 8px; border-left: 2px solid #d8dee8; background: #f6f8fb; color: #4e5b70; font-size: 11px; font-weight: 400; line-height: 1.65; overflow-wrap: anywhere; }
</style>
