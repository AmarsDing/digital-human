<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'
import {
  deleteTTSFile,
  getVoices,
  importTTSFolder,
  listTTSFiles,
  previewVoice,
  setVoice,
  uploadTTSFile,
  type MediaFile,
  type Voice
} from '../../services/api/panels'

const props = defineProps<{ voiceId: string }>()
const emit = defineEmits<{
  (e: 'select', id: string): void
  (e: 'preview', text: string): void
}>()

const voices = ref<Voice[]>([])
const current = ref('')
const activeBase = ref('')
const files = ref<MediaFile[]>([])
const filesDir = ref('')
const folder = ref('')
const uploadInput = ref<HTMLInputElement | null>(null)
const draft = ref('')
const previewText = ref('你好，我是小雅。')
const loading = ref(false)
const previewing = ref(false)
const error = ref('')
const note = ref('')
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
    current.value = info.current
    activeBase.value = info.active_base
    if (!draft.value) draft.value = info.current || props.voiceId
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function apply(id = draft.value): Promise<void> {
  const name = id.trim()
  if (!name) return
  error.value = ''
  try {
    await setVoice(name)
    current.value = name
    draft.value = name
    emit('select', name)
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function preview(): Promise<void> {
  if (!previewText.value.trim()) return
  previewing.value = true
  error.value = ''
  try {
    const blob = await previewVoice(previewText.value.trim(), draft.value.trim())
    stopAudio()
    audioUrl = URL.createObjectURL(blob)
    audio = new Audio(audioUrl)
    await audio.play()
    emit('preview', previewText.value.trim())
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    previewing.value = false
  }
}

async function loadFiles(): Promise<void> {
  try {
    const r = await listTTSFiles()
    files.value = r.files
    filesDir.value = r.dir
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function upload(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  if (!f) return
  try {
    await uploadTTSFile(f)
    await loadFiles()
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    input.value = ''
  }
}

async function importFolder(): Promise<void> {
  if (!folder.value.trim()) return
  try {
    const r = await importTTSFolder(folder.value.trim())
    folder.value = ''
    await loadFiles()
    note.value = `已导入 ${r.imported}，跳过 ${r.skipped}`
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

const pendingDelete = ref('')
async function removeFile(name: string): Promise<void> {
  pendingDelete.value = ''
  try {
    await deleteTTSFile(name)
    await loadFiles()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

watch(
  () => props.voiceId,
  (v) => {
    if (v && !draft.value) draft.value = v
  }
)

onMounted(() => {
  void load()
  void loadFiles()
})
onBeforeUnmount(stopAudio)
</script>

<template>
  <div class="voice">
    <div class="bar">
      <span class="mono">当前：{{ current || props.voiceId || '未设置' }}</span>
      <button type="button" class="mini" :disabled="loading" @click="load">刷新</button>
    </div>
    <p v-if="loading" class="note">正在读取音色列表…</p>
    <p v-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>
    <p v-if="!loading && voices.length === 0" class="note">后端未返回可用音色（OmniVoice fake 端口已就绪，待真实边车）。</p>
    <ul v-if="!loading && voices.length > 0" class="list">
      <li v-for="v in voices" :key="v.name" :class="{ active: v.name === current }">
        <button type="button" class="item" @click="apply(v.name)">
          <span>{{ v.label || v.name }}</span>
          <span class="mono">{{ v.name }}</span>
        </button>
      </li>
    </ul>
    <label class="row">
      <span>音色 ID</span>
      <input v-model="draft" type="text" placeholder="voice preset id" @keyup.enter="apply()" />
      <button type="button" :disabled="!draft.trim()" @click="apply()">应用</button>
    </label>
    <label class="row">
      <span>试听文本</span>
      <input v-model="previewText" type="text" />
    </label>
    <button class="preview" type="button" :disabled="previewing || !previewText.trim()" @click="preview">
      {{ previewing ? '合成中…' : '试听' }}
    </button>
    <p class="mono base">音色库：{{ activeBase || '—' }}</p>

    <section class="files">
      <div class="bar">
        <span class="mono">音频库 {{ files.length }} 个 · {{ filesDir || '—' }}</span>
      </div>
      <p v-if="files.length === 0" class="note">音频库为空，可上传或导入同名 WAV。</p>
      <ul v-else class="flist">
        <li v-for="f in files" :key="f.name">
          <span class="fname">{{ f.name }}</span>
          <span class="mono">{{ Math.round(f.size / 1024) }}K</span>
          <button type="button" class="mini" @click="pendingDelete = f.name">删除</button>
        </li>
      </ul>
      <div class="fres">
        <input ref="uploadInput" type="file" accept="audio/*,.wav,.mp3" hidden @change="upload" />
        <button type="button" class="mini" @click="uploadInput?.click()">上传</button>
        <input v-model="folder" type="text" placeholder="导入目录绝对路径" @keyup.enter="importFolder" />
        <button type="button" class="mini" :disabled="!folder.trim()" @click="importFolder">导入</button>
      </div>
    </section>
    <NtConfirmDialog
      :open="!!pendingDelete"
      title="删除音频文件"
      :message="`将删除「${pendingDelete}」，删除后不可恢复。确定继续？`"
      confirm-text="删除"
      @confirm="removeFile(pendingDelete)"
      @cancel="pendingDelete = ''"
    />
  </div>
</template>

<style scoped>
.voice {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.note {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
}
.error {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 180px;
  overflow: auto;
}
.item {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.active .item {
  border-color: var(--nt-accent-border);
  color: var(--nt-text-0);
}
.row {
  display: grid;
  grid-template-columns: 68px minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.row input {
  min-width: 0;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.row input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
}
.row button,
.mini,
.preview {
  height: 32px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.preview:disabled,
.row button:disabled,
.mini:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
.base {
  margin: 0;
}
.files {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding-top: 7px;
  border-top: 1px solid var(--nt-border-1);
}
.flist {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 130px;
  overflow: auto;
}
.flist li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.fname {
  overflow: hidden;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.row input[type='text'] {
  flex: 1;
}
.ok {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-micro);
}
.fres {
  display: flex;
  gap: 6px;
}
.fres input[type='text'] {
  flex: 1;
  min-width: 0;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.fres input[type='text']:focus {
  outline: none;
  border-color: var(--nt-accent-border);
}
</style>
