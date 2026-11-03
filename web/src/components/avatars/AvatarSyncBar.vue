<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import NtButton from '../ui/NtButton.vue'
import { useAvatarsStore } from '../../stores/avatars'
import { classifyApiError } from '../../services/api/client'
import type { AvatarDetail } from '../../services/api/panels'

// sync 状态与按钮（v3 §2.2）：轮询 2s / 总超时 480s 在 store.syncAndWait；
// 按钮 loading 期间禁用防连点；超时/失败转错误态可手动重试。
const props = defineProps<{ avatar: AvatarDetail }>()
const emit = defineEmits<{ (e: 'synced'): void }>()

const store = useAvatarsStore()
const error = ref('')
let controller: AbortController | null = null

const running = computed(() => !!store.syncing[props.avatar.persona_id])

const stateLabel = computed(() => {
  const s = props.avatar.vidu_sync_state?.state
  switch (s) {
    case 'done':
      return '已同步'
    case 'error':
      return `同步失败${props.avatar.vidu_sync_state?.error ? `：${props.avatar.vidu_sync_state.error}` : ''}`
    case 'queued':
      return '排队中'
    case 'uploading':
      return '上传素材中'
    case 'creating':
      return '创建 Vidu 形象中'
    default:
      return '未同步'
  }
})
const tone = computed(() => {
  const s = props.avatar.vidu_sync_state?.state
  if (s === 'done') return 'ok'
  if (s === 'error') return 'bad'
  if (s === 'queued' || s === 'uploading' || s === 'creating') return 'run'
  return 'muted'
})

const expiry = computed(() => {
  const ms = props.avatar.expires_at_ms
  if (!ms) return ''
  const days = Math.ceil((ms - Date.now()) / 86400000)
  return days < 0 ? 'Vidu 资产已过期' : `Vidu 资产 ${days} 天后过期`
})

async function sync(): Promise<void> {
  if (running.value) return
  error.value = ''
  controller?.abort()
  controller = new AbortController()
  try {
    const st = await store.syncAndWait(props.avatar.persona_id, controller.signal)
    if (st.state === 'error') {
      error.value = st.error || '同步失败'
    } else {
      emit('synced')
    }
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') return
    error.value = classifyApiError(err).message
  }
}

onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <section class="sync" aria-label="Vidu 同步">
    <div class="sync__head">
      <span class="sync__label">Vidu 同步</span>
      <span class="pill" :class="`pill--${tone}`" aria-live="polite">{{ stateLabel }}</span>
    </div>
    <p v-if="expiry" class="sync__meta mono">{{ expiry }}</p>
    <p v-if="avatar.vidu_sync_state?.job_id" class="sync__meta mono">job {{ avatar.vidu_sync_state.job_id }}</p>

    <div class="sync__ops">
      <NtButton size="sm" variant="primary" :disabled="running || !avatar.avatar_asset_id" @click="sync">
        {{ running ? '同步中…' : avatar.vidu_sync_state?.state === 'done' ? '重新同步' : '同步到 Vidu' }}
      </NtButton>
      <span v-if="!avatar.avatar_asset_id" class="sync__hint">需先上传形象图</span>
      <span v-else class="sync__hint">同步需数分钟，期间可离开本页</span>
    </div>
    <p v-if="error" class="sync__err" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.sync {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sync__head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sync__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.pill {
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.pill--ok {
  border-color: var(--nt-success-border);
  color: var(--nt-success);
}
.pill--bad {
  border-color: var(--nt-danger-border);
  color: var(--nt-danger);
}
.pill--run {
  border-color: var(--nt-accent-border);
  color: var(--nt-accent);
}
.sync__meta {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.sync__ops {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sync__ops :deep(.nt-btn) {
  min-height: 44px;
}
.sync__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.sync__err {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.mono {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
}
</style>
