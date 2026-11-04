<script setup lang="ts">
import { computed } from 'vue'
import NtButton from "../ui/NtButton.vue"
import NtDrawer from '../ui/NtDrawer.vue'
import type { LivePreflightResult } from '../../services/api/live'
const props = defineProps<{ open: boolean; result: LivePreflightResult | null; busy: boolean; error: string }>()
defineEmits<{ (e:'close'):void; (e:'retry'):void; (e:'start'):void; (e:'prepare'):void }>()
const names: Record<string,string> = { avatar_resolve:'形象与音色',content_package:'场次草稿',action_sender:'动作发送',koubo_actiongen:'口播动作生成',content_storage:'内容存储',song_player:'歌曲播放',tts:'语音合成',vidu:'数字人视频',push_gate:'平台推流',storage_persistent:'数据持久化' }
const labels: Record<string,string> = { pass:'通过',warn:'提醒',fail:'未通过',unknown:'待验证',running:'检测中' }
const failCount = computed(() => props.result?.checks.filter((check) => check.status === 'fail').length ?? 0)
const warnCount = computed(() => props.result?.checks.filter((check) => check.status === 'warn').length ?? 0)
const pendingCount = computed(() => props.result?.checks.filter((check) => check.status === 'unknown' || check.status === 'running').length ?? 0)
</script>
<template>
  <NtDrawer :open="open" title="开播预检" :width="640" @close="$emit('close')">
    <p v-if="busy" aria-live="polite">正在检测，尚未开播…</p>
    <p v-if="error" class="preflight-error" role="alert">{{ error }}</p>
    <template v-if="result">
      <p role="status">{{ result.ready ? '预检通过，请明确确认开始直播。' : '还有条件未满足，请处理后重新预检。' }}</p>
      <p>{{ result.mode === 'local-preview' ? '本地预览，允许模拟能力；不代表公开直播可用。' : '公开直播，要求真实能力和持久化。' }}</p>
      <p class="preflight-summary" aria-label="本次会话预检结果计数">
        本次会话：失败 <strong data-status="fail">{{ failCount }}</strong> ·
        提醒 <strong data-status="warn">{{ warnCount }}</strong> ·
        待验证 <strong data-status="unknown">{{ pendingCount }}</strong>
      </p>
      <ul class="preflight-checks"><li v-for="check in result.checks" :key="check.code" :data-status="check.status">
        <span>{{ names[check.code] || '其他检查' }}</span><strong>{{ labels[check.status] || '待验证' }}</strong>
        <details v-if="check.detail"><summary>查看原因</summary><p>{{ check.detail }}</p></details>
      </li></ul>
    </template>
    <template #footer><div class="preflight-actions">
      <NtButton type="button" :disabled="busy" @click="$emit('prepare')">场次准备</NtButton>
      <NtButton type="button" :disabled="busy" @click="$emit('retry')">重新预检</NtButton>
      <NtButton variant="primary" type="button" :disabled="busy || !result?.ready" @click="$emit('start')">开始直播</NtButton>
    </div></template>
  </NtDrawer>
</template>
<style scoped>
p { color:var(--nt-text-2); line-height:1.6; overflow-wrap:anywhere; }
.preflight-error { color:var(--nt-danger); }
.preflight-checks { list-style:none; padding:0; display:grid; gap:12px; }
li { padding:12px; border:1px solid var(--nt-border-1); border-radius:var(--nt-radius-m); background:var(--nt-data-bg); display:flex; flex-wrap:wrap; gap:12px; justify-content:space-between; }
li[data-status=pass] strong { color:var(--nt-success); } li[data-status=warn] strong { color:var(--nt-warn); } li[data-status=fail] strong { color:var(--nt-danger); }
details { width:100%; } summary { min-height:44px; cursor:pointer; }
.preflight-actions { display:flex; flex-wrap:wrap; gap:12px; }
</style>
