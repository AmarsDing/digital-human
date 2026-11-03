<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'
import {
  deleteSong,
  getSongStatus,
  listSongs,
  playSong,
  stopSong,
  uploadSong,
  type MediaFile,
  type SongState
} from '../../services/api/panels'

const emit = defineEmits<{
  (e: 'play', file: string): void
  (e: 'stop'): void
}>()

const files = ref<MediaFile[]>([])
const status = ref<SongState | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const note = ref('')
const script = ref('')
const draft = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [list, st] = await Promise.all([listSongs(), getSongStatus()])
    files.value = list
    status.value = st
    if (st.song_playing && !draft.value) draft.value = st.file
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function upload(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  if (!f) return
  busy.value = true
  error.value = ''
  try {
    await uploadSong(f)
    note.value = `已上传 ${f.name}`
    await load()
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
    input.value = ''
  }
}

async function play(f: MediaFile): Promise<void> {
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    status.value = await playSong(f.name, script.value)
    draft.value = f.name
    note.value = `正在播放 ${f.name}`
    emit('play', f.name)
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

async function stop(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    status.value = await stopSong()
    note.value = '已停止播放'
    emit('stop')
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

const pendingDelete = ref('')
async function remove(): Promise<void> {
  const name = pendingDelete.value
  pendingDelete.value = ''
  try {
    await deleteSong(name)
    await load()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

onMounted(load)
</script>

<template>
  <div class="songs">
    <div class="bar">
      <span class="mono">
        {{ status?.song_playing ? `播放中 ${status.file}` : '未播放' }} · {{ files.length }} 首
      </span>
      <button type="button" class="mini" :disabled="loading" @click="load">刷新</button>
    </div>
    <p v-if="loading" class="note">正在读取歌曲库…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <p v-if="files.length === 0" class="note">歌曲库为空，可上传 mp3/wav（后端 fake 播放器只记录调用）。</p>
    <ul v-else class="list">
      <li v-for="f in files" :key="f.name" :class="{ playing: status?.file === f.name && status?.song_playing }">
        <span class="name">{{ f.name }}</span>
        <span class="mono">{{ Math.round(f.size / 1024) }}K / {{ f.lib }}</span>
        <button type="button" class="mini" :disabled="busy" @click="play(f)">点播</button>
        <button type="button" class="mini" :disabled="busy" @click="pendingDelete = f.name">删除</button>
      </li>
    </ul>

    <label class="row">
      <span class="mono">歌词/脚本</span>
      <input v-model="script" type="text" placeholder="可选：逐行歌词" />
    </label>
    <label class="row">
      <span class="mono">当前</span>
      <input v-model="draft" type="text" placeholder="当前歌曲文件" readonly />
    </label>
    <div class="row btns">
      <input ref="fileInput" type="file" accept="audio/*,.mp3,.wav,.flac,.m4a" hidden @change="upload" />
      <button type="button" class="mini" :disabled="busy" @click="fileInput?.click()">上传歌曲</button>
      <button type="button" class="mini" :disabled="busy || !status?.song_playing" @click="stop">停止</button>
    </div>
    <NtConfirmDialog
      :open="!!pendingDelete"
      title="删除歌曲"
      :message="`将删除「${pendingDelete}」，删除后不可恢复。确定继续？`"
      confirm-text="删除"
      @confirm="remove"
      @cancel="pendingDelete = ''"
    />
  </div>
</template>

<style scoped>
.songs {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.note,
.ok,
.error {
  margin: 0;
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
  color: var(--nt-text-3);
}
.ok {
  color: var(--nt-text-1);
}
.error {
  color: var(--nt-danger);
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
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
.list li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto auto;
  align-items: center;
  gap: 6px;
  padding: 5px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.list li.playing {
  border-color: var(--nt-accent-border);
}
.name {
  overflow: hidden;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.row input {
  flex: 1;
  min-width: 0;
  height: 30px;
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
.mini {
  height: 30px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.mini:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.btns {
  flex-wrap: wrap;
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>
