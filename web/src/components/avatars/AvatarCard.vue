<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import NtButton from '../ui/NtButton.vue'
import { fetchAvatarImage, type AvatarListItem } from '../../services/api/panels'
import { canAllocateBlob, registerBlob, releaseBlob, touchBlob } from '../../services/blob-pool'
import { canActivateAvatar } from '../../utils/avatar-validate'

const props = defineProps<{ avatar: AvatarListItem }>()
const emit = defineEmits<{
  (e: 'edit', id: string): void
  (e: 'activate', id: string): void
  (e: 'duplicate', id: string): void
  (e: 'remove', id: string): void
}>()

const poolKey = computed(() => `avatar-list:${props.avatar.persona_id}`)
const root = ref<HTMLElement | null>(null)
const imgUrl = ref('')
const imgState = ref<'idle' | 'loading' | 'ready' | 'placeholder' | 'empty' | 'error'>('idle')
let observer: IntersectionObserver | null = null
let inFlight: AbortController | null = null

// 懒加载：进入视口才 fetch；预算满则显示占位 + 点击加载（v3 §9.2，同屏 blob ≤8）。
async function loadImage(force = false): Promise<void> {
  if (imgUrl.value || imgState.value === 'loading') return
  if (!force && !canAllocateBlob()) {
    imgState.value = 'placeholder'
    return
  }
  imgState.value = 'loading'
  inFlight?.abort()
  inFlight = new AbortController()
  try {
    const blob = await fetchAvatarImage(props.avatar.persona_id, inFlight.signal)
    const url = URL.createObjectURL(blob)
    registerBlob(poolKey.value, url, () => {
      // 被 LRU 驱逐：回退占位态，等再次进入视口或点击加载。
      imgUrl.value = ''
      imgState.value = 'placeholder'
    })
    imgUrl.value = url
    imgState.value = 'ready'
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      imgState.value = 'idle'
      return
    }
    imgState.value = 'empty'
  }
}

function onVisible(entries: IntersectionObserverEntry[]): void {
  if (entries.some((e) => e.isIntersecting)) {
    touchBlob(poolKey.value)
    void loadImage()
  }
}

onMounted(() => {
  if (typeof IntersectionObserver !== 'undefined' && root.value) {
    observer = new IntersectionObserver(onVisible, { rootMargin: '80px' })
    observer.observe(root.value)
  } else {
    void loadImage()
  }
})

// revoke 时机一：组件卸载。
onBeforeUnmount(() => {
  observer?.disconnect()
  inFlight?.abort()
  releaseBlob(poolKey.value)
})

const syncLabel = computed(() => {
  switch (props.avatar.vidu_sync_state?.state) {
    case 'done':
      return '已同步'
    case 'error':
      return '同步失败'
    case 'queued':
      return '排队中'
    case 'uploading':
      return '上传中'
    case 'creating':
      return '创建中'
    default:
      return '未同步'
  }
})
const syncTone = computed(() => {
  const s = props.avatar.vidu_sync_state?.state
  if (s === 'done') return 'ok'
  if (s === 'error') return 'bad'
  if (s === 'queued' || s === 'uploading' || s === 'creating') return 'run'
  return 'muted'
})

// 到期倒计时：≤7 天告警（v2 §6.2 提前手动同步提示）。
const expiry = computed(() => {
  const ms = props.avatar.expires_at_ms
  if (!ms) return null
  const days = Math.ceil((ms - Date.now()) / 86400000)
  if (days < 0) return { text: '已过期', warn: true }
  if (days <= 7) return { text: `${days} 天后过期`, warn: true }
  return { text: `${days} 天后过期`, warn: false }
})

const activateBlock = computed(() => canActivateAvatar(props.avatar))
</script>

<template>
  <article ref="root" class="card" :class="{ 'card--active': avatar.is_active }">
    <button type="button" class="card__thumb" :aria-label="`预览 ${avatar.name} 形象图`" @click="emit('edit', avatar.persona_id)">
      <img v-if="imgUrl" :src="imgUrl" :alt="`${avatar.name} 形象图`" class="card__img" />
      <span v-else-if="imgState === 'loading'" class="card__ph">加载中…</span>
      <span v-else-if="imgState === 'placeholder'" class="card__ph card__ph--btn" role="button" tabindex="0" @click.stop="loadImage(true)" @keydown.enter.stop="loadImage(true)">
        点击加载图片
      </span>
      <span v-else class="card__ph">暂无形象图</span>
      <span v-if="avatar.is_active" class="card__badge">当前</span>
    </button>

    <div class="card__body">
      <div class="card__title">
        <strong>{{ avatar.name }}</strong>
        <span class="mono">{{ avatar.slug }}</span>
      </div>
      <div class="card__meta">
        <span class="pill" :class="`pill--${syncTone}`" aria-live="polite">{{ syncLabel }}</span>
        <span v-if="expiry" class="mono" :class="{ warn: expiry.warn }">{{ expiry.text }}</span>
      </div>
    </div>

    <div class="card__actions">
      <NtButton size="sm" @click="emit('edit', avatar.persona_id)">编辑</NtButton>
      <NtButton size="sm" variant="primary" :disabled="avatar.is_active || !!activateBlock" :title="activateBlock || '设为当前档案'" @click="emit('activate', avatar.persona_id)">
        设为当前
      </NtButton>
      <NtButton size="sm" @click="emit('duplicate', avatar.persona_id)">复制</NtButton>
      <NtButton size="sm" variant="danger" :disabled="avatar.is_active" :title="avatar.is_active ? '当前使用中的档案不能删除' : '删除'" @click="emit('remove', avatar.persona_id)">
        删除
      </NtButton>
    </div>
  </article>
</template>

<style scoped>
.card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  overflow: hidden;
}
.card--active {
  border-color: var(--nt-accent-border);
  box-shadow: var(--nt-glow-soft);
}
.card__thumb {
  position: relative;
  display: block;
  width: 100%;
  aspect-ratio: 1 / 1;
  padding: 0;
  border: none;
  border-bottom: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  cursor: pointer;
}
.card__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.card__ph {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.card__ph--btn {
  color: var(--nt-accent);
}
.card__badge {
  position: absolute;
  top: 8px;
  right: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
  font-size: var(--nt-fs-micro);
}
.card__body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
}
.card__title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
.card__title strong {
  color: var(--nt-text-1);
  font-size: var(--nt-fs-panel);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.card__meta {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
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
.mono {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
  font-size: var(--nt-fs-micro);
  color: var(--nt-text-3);
}
.warn {
  color: var(--nt-warn);
}
.card__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 0 12px 12px;
}
/* 触屏目标 ≥44px（AGENTS.md §6-10），覆盖 NtButton sm 36px 高度。 */
.card__actions :deep(.nt-btn) {
  min-height: 44px;
}
</style>
