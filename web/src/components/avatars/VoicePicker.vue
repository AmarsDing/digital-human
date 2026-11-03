<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import NtButton from '../ui/NtButton.vue'
import NtSelect from '../ui/NtSelect.vue'
import { getVoices, previewVoice, type Voice } from '../../services/api/panels'
import { classifyApiError } from '../../services/api/client'

// 音色选择 + 试听（v3 §9.3）：单例 Audio，新试听打断旧试听（先 pause 再换 src）；
// 失败行内提示（role=alert）。voice_id 选填，未选时开播预检拦截（后端 400）。
const props = defineProps<{ modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const voices = ref<Voice[]>([])
const options = ref<{ label: string; value: string }[]>([])
const loading = ref(false)
const previewing = ref(false)
const previewText = ref('你好，我是小雅。')
const error = ref('')
const sidecarDown = ref(false)

// 模块级单例：新试听打断旧试听。
let audio: HTMLAudioElement | null = null
let audioUrl = ''
function stopAudio(): void {
  if (audio) {
    audio.pause()
    audio = null
  }
  if (audioUrl) {
    URL.revokeObjectURL(audioUrl)
    audioUrl = ''
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const info = await getVoices()
    voices.value = info.voices
    options.value = info.voices.map((v) => ({ label: v.label || v.name, value: v.name }))
    sidecarDown.value = false
  } catch (err) {
    sidecarDown.value = true
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function preview(): Promise<void> {
  if (previewing.value) return
  previewing.value = true
  error.value = ''
  try {
    const blob = await previewVoice(previewText.value.trim() || '你好。', props.modelValue)
    stopAudio()
    audioUrl = URL.createObjectURL(blob)
    audio = new Audio(audioUrl)
    await audio.play()
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    previewing.value = false
  }
}

onMounted(() => void load())
onBeforeUnmount(stopAudio)
</script>

<template>
  <section class="voice" aria-label="音色">
    <div class="voice__head">
      <span class="voice__label">音色</span>
      <span class="voice__hint">选填；未选择时开播将被预检拦截</span>
    </div>

    <p v-if="loading" class="voice__note">正在读取音色列表…</p>
    <template v-else>
      <NtSelect
        v-if="options.length"
        label="音色预设"
        :model-value="modelValue"
        :options="[{ label: '（未选择）', value: '' }, ...options]"
        @update:model-value="emit('update:modelValue', String($event))"
      />
      <p v-else class="voice__note">
        {{ sidecarDown ? 'OmniVoice 边车不可达，仅可手动填写音色 ID' : '暂无可用音色' }}
      </p>
      <label v-if="sidecarDown" class="voice__manual">
        <span class="voice__label">音色 ID</span>
        <input
          type="text"
          :value="modelValue"
          @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
        />
      </label>
    </template>

    <div class="voice__preview">
      <input v-model="previewText" type="text" aria-label="试听文本" />
      <NtButton size="sm" :disabled="previewing || !modelValue" @click="preview">
        {{ previewing ? '合成中…' : '试听' }}
      </NtButton>
    </div>
    <p v-if="error" class="voice__err" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.voice {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.voice__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.voice__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.voice__hint,
.voice__note {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
  margin: 0;
}
.voice__manual {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.voice__manual input {
  height: 42px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.voice__manual input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.voice__preview {
  display: flex;
  gap: 8px;
  align-items: center;
}
.voice__preview input {
  flex: 1;
  min-width: 0;
  height: 44px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.voice__preview input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.voice__preview :deep(.nt-btn) {
  min-height: 44px;
}
.voice__err {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
</style>
