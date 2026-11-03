<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import NtButton from '../ui/NtButton.vue'
import { fetchAvatarImage, uploadAvatarImage } from '../../services/api/panels'
import { classifyApiError } from '../../services/api/client'
import { registerBlob, releaseBlob } from '../../services/blob-pool'
import { IMAGE_ACCEPT, validateImageFile, validateImagePixels } from '../../utils/avatar-validate'

// 形象图上传（v3 §9.3）：不自动重试，失败给手动重试按钮；AbortController 中断；
// revoke 时机：换图（新 blob 就绪后立即 revoke 旧）与组件卸载。
const props = defineProps<{
  personaId: string // 新建时为空，上传后仅靠本地 blob 预览
  assetId?: string
}>()
const emit = defineEmits<{ (e: 'uploaded', assetId: string): void; (e: 'preview', url: string): void }>()

const fileInput = ref<HTMLInputElement | null>(null)
const previewUrl = ref('')
const imgSource = ref<'none' | 'remote' | 'local'>('none')
const uploading = ref(false)
const error = ref('')
const lastFile = ref<File | null>(null)
let controller: AbortController | null = null
// 服务端已有图与本地新选图各用固定 key，换图时 registerBlob 同 key 先释放旧值。
const remoteKey = computed(() => `avatar-edit:${props.personaId || 'new'}`)
const localKey = 'avatar-picked:edit'

async function showRemote(): Promise<void> {
  if (!props.personaId || !props.assetId) return
  try {
    const blob = await fetchAvatarImage(props.personaId)
    const url = URL.createObjectURL(blob)
    registerBlob(remoteKey.value, url, () => {
      if (imgSource.value === 'remote') previewUrl.value = ''
    })
    previewUrl.value = url
  } catch {
    previewUrl.value = ''
  }
}

watch(
  () => props.assetId,
  () => {
    if (imgSource.value === 'local') return // 已选择新图待保存
    imgSource.value = props.assetId ? 'remote' : 'none'
    previewUrl.value = ''
    void showRemote()
  },
  { immediate: true }
)

function checkPixels(file: File): Promise<string> {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => {
      const msg = validateImagePixels(img.naturalWidth, img.naturalHeight)
      URL.revokeObjectURL(url)
      resolve(msg)
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
  if (!f) return
  // 进入处理流程即置 busy：覆盖校验+上传全程，期间连选第二文件直接拒绝并提示（R3-03）。
  if (uploading.value) {
    error.value = '上一张图片仍在处理中，请稍候再试'
    return
  }
  uploading.value = true
  error.value = ''
  try {
    error.value = validateImageFile(f) || (await checkPixels(f))
    if (error.value) {
      // 校验失败清空待传文件：重试按钮只对上传失败开放，避免误传旧文件（R3-06）。
      lastFile.value = null
      return
    }
    lastFile.value = f
    // 本地预览先行（换图：registerBlob 同 key 自动 revoke 旧 blob）。
    const url = URL.createObjectURL(f)
    registerBlob(localKey, url, () => {
      if (imgSource.value === 'local') {
        imgSource.value = 'none'
        previewUrl.value = ''
      }
    })
    imgSource.value = 'local'
    previewUrl.value = url
    await doUpload()
  } finally {
    uploading.value = false
  }
}

async function doUpload(): Promise<void> {
  if (!lastFile.value) return
  controller?.abort()
  controller = new AbortController()
  try {
    const r = await uploadAvatarImage(lastFile.value, controller.signal)
    error.value = '' // 清掉窗口期拒绝等瞬时提示
    emit('uploaded', r.asset_id)
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') return
    error.value = classifyApiError(err).message
  }
}

// 上传失败手动重试（校验失败不出现重试入口，见 pick 分支）。
async function retry(): Promise<void> {
  if (!lastFile.value || uploading.value) return
  uploading.value = true
  error.value = ''
  try {
    await doUpload()
  } finally {
    uploading.value = false
  }
}

onBeforeUnmount(() => {
  controller?.abort()
  releaseBlob(remoteKey.value)
  releaseBlob(localKey)
})

const shown = computed(() => previewUrl.value)
</script>

<template>
  <section class="picker" aria-label="形象图">
    <div class="picker__head">
      <span class="picker__label">形象图 <i class="req" aria-hidden="true">*</i></span>
      <span class="picker__hint">PNG / JPEG，≤10MB，≤4096×4096；未同步 Vidu 不允许设为当前</span>
    </div>

    <div class="picker__row">
      <button
        type="button"
        class="picker__thumb"
        :disabled="!shown"
        :aria-label="shown ? '查看大图' : '暂无形象图'"
        @click="shown && emit('preview', shown)"
      >
        <img v-if="shown" :src="shown" alt="形象图预览" class="picker__img" />
        <span v-else class="picker__ph">暂无形象图</span>
      </button>

      <div class="picker__ops">
        <input ref="fileInput" type="file" :accept="IMAGE_ACCEPT" :disabled="uploading" hidden @change="pick" />
        <NtButton size="sm" :disabled="uploading" @click="fileInput?.click()">
          {{ uploading ? '上传中…' : shown ? '更换图片' : '选择图片' }}
        </NtButton>
        <NtButton v-if="error && lastFile" size="sm" :disabled="uploading" @click="retry">重试上传</NtButton>
        <p v-if="error" class="picker__err" role="alert">{{ error }}</p>
        <p v-else-if="assetId || imgSource === 'local'" class="picker__ok" aria-live="polite">已选择形象图，保存档案后生效</p>
      </div>
    </div>
  </section>
</template>

<style scoped>
.picker {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.picker__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.picker__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.req {
  color: var(--nt-danger);
  font-style: normal;
}
.picker__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.picker__row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.picker__thumb {
  width: 128px;
  height: 128px;
  flex: none;
  padding: 0;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-inset);
  overflow: hidden;
  cursor: pointer;
}
.picker__thumb:disabled {
  cursor: default;
}
.picker__thumb:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
.picker__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.picker__ph {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.picker__ops {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.picker__ops :deep(.nt-btn) {
  min-height: 44px;
}
.picker__err {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.picker__ok {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>
