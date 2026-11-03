<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import NtButton from '../ui/NtButton.vue'
import {
  fetchAvatarRefImage,
  removeAvatarRef,
  uploadAvatarRef,
  type AvatarRefItem
} from '../../services/api/panels'
import { classifyApiError } from '../../services/api/client'
import { registerBlob, releaseBlob } from '../../services/blob-pool'
import { IMAGE_ACCEPT, REF_MAX, validateImageFile, validateImagePixels } from '../../utils/avatar-validate'

// 参考图管理（≤3 有序，v3 §9.1）：上传/移除即时调后端（需档案已存在），
// 缩略图经 blob-pool 计数；组件卸载 revoke 全部缩略图。
const props = defineProps<{
  personaId: string // 空 = 档案未保存，整体禁用
  refs: AvatarRefItem[]
}>()
const emit = defineEmits<{
  (e: 'change'): void // 通知父级重新拉详情
  (e: 'preview', url: string): void
}>()

const fileInput = ref<HTMLInputElement | null>(null)
const busy = ref(false)
const error = ref('')
const thumbs = ref<Record<string, string>>({})
let controller: AbortController | null = null

function refKey(assetId: string): string {
  return `avatar-ref:${props.personaId}:${assetId}`
}

async function loadThumbs(): Promise<void> {
  for (const r of props.refs) {
    if (thumbs.value[r.asset_id]) continue
    try {
      const blob = await fetchAvatarRefImage(props.personaId, r.asset_id)
      const url = URL.createObjectURL(blob)
      registerBlob(refKey(r.asset_id), url, () => {
        const next = { ...thumbs.value }
        delete next[r.asset_id]
        thumbs.value = next
      })
      thumbs.value = { ...thumbs.value, [r.asset_id]: url }
    } catch {
      // 单张失败不阻断其他缩略图。
    }
  }
}

watch(
  () => [props.personaId, props.refs.map((r) => r.asset_id).join(',')],
  () => void loadThumbs(),
  { immediate: true }
)

function checkPixels(file: File): Promise<string> {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => {
      URL.revokeObjectURL(url)
      resolve(validateImagePixels(img.naturalWidth, img.naturalHeight))
    }
    img.onerror = () => {
      URL.revokeObjectURL(url)
      resolve('无法解析图片，请更换文件')
    }
    img.src = url
  })
}

async function pick(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = ''
  if (!f || !props.personaId) return
  error.value = validateImageFile(f) || (await checkPixels(f))
  if (error.value) return
  busy.value = true
  controller?.abort()
  controller = new AbortController()
  try {
    await uploadAvatarRef(props.personaId, f, controller.signal)
    emit('change')
  } catch (err) {
    if (!(err instanceof DOMException && err.name === 'AbortError')) {
      error.value = classifyApiError(err).message
    }
  } finally {
    busy.value = false
  }
}

async function remove(assetId: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await removeAvatarRef(props.personaId, assetId)
    releaseBlob(refKey(assetId))
    emit('change')
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

onBeforeUnmount(() => {
  controller?.abort()
  for (const r of props.refs) releaseBlob(refKey(r.asset_id))
})
</script>

<template>
  <section class="refs" aria-label="参考图">
    <div class="refs__head">
      <span class="refs__label">参考图（{{ refs.length }}/{{ REF_MAX }}，按顺序推送）</span>
      <span class="refs__hint">PNG / JPEG，≤10MB；开播后按序推送到会话</span>
    </div>

    <ul v-if="refs.length" class="refs__list">
      <li v-for="r in refs" :key="r.asset_id" class="refs__item">
        <span class="refs__ord mono">{{ r.ord + 1 }}</span>
        <button type="button" class="refs__thumb" :aria-label="`查看参考图 ${r.ord + 1}`" @click="thumbs[r.asset_id] && emit('preview', thumbs[r.asset_id])">
          <img v-if="thumbs[r.asset_id]" :src="thumbs[r.asset_id]" :alt="`参考图 ${r.ord + 1}`" class="refs__img" />
          <span v-else class="refs__ph">…</span>
        </button>
        <NtButton size="sm" variant="danger" :disabled="busy" @click="remove(r.asset_id)">移除</NtButton>
      </li>
    </ul>
    <p v-else class="refs__empty">暂无参考图</p>

    <div class="refs__ops">
      <input ref="fileInput" type="file" :accept="IMAGE_ACCEPT" hidden @change="pick" />
      <NtButton size="sm" :disabled="busy || !personaId || refs.length >= REF_MAX" @click="fileInput?.click()">
        {{ busy ? '处理中…' : '上传参考图' }}
      </NtButton>
      <span v-if="!personaId" class="refs__hint">保存档案后可管理参考图</span>
      <span v-else-if="refs.length >= REF_MAX" class="refs__hint">已达 3 张上限</span>
    </div>
    <p v-if="error" class="refs__err" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.refs {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.refs__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.refs__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.refs__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.refs__list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.refs__item {
  display: flex;
  align-items: center;
  gap: 10px;
}
.refs__ord {
  width: 20px;
  text-align: center;
  color: var(--nt-text-3);
}
.refs__thumb {
  width: 56px;
  height: 56px;
  padding: 0;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
  overflow: hidden;
  cursor: pointer;
}
.refs__thumb:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
.refs__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.refs__ph {
  color: var(--nt-text-3);
}
.refs__empty {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.refs__ops {
  display: flex;
  align-items: center;
  gap: 10px;
}
.refs__ops :deep(.nt-btn) {
  min-height: 44px;
}
.refs__err {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.mono {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
}
</style>
