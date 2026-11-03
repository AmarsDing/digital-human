<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError, classifyApiError } from '../../services/api/client'
import { useRefsDraftStore } from '../../stores/refsDraft'
import {
  getRefs,
  listRefFiles,
  removeRefs,
  switchRefs,
  uploadRefFile,
  type RefFile,
  type RefsImage,
  type RefsState
} from '../../services/api/panels'

const draft = useRefsDraftStore()

const state = ref<RefsState | null>(null)
const files = ref<RefFile[]>([])
const url = ref('')
const imageId = ref('')
// 主状态与文件列表拆分（v2 §5.1）：各自 loading/error/重试，互不耦合。
const stateLoading = ref(false)
const stateError = ref('')
const filesLoading = ref(false)
const filesError = ref('')
const busy = ref(false)
const restoring = ref(false)
const uploading = ref(false)
const note = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

async function loadState(): Promise<void> {
  stateLoading.value = true
  stateError.value = ''
  try {
    state.value = await getRefs()
    draft.syncFromServer(state.value.images)
  } catch (err) {
    stateError.value = classifyApiError(err).message
  } finally {
    stateLoading.value = false
  }
}

async function loadFiles(): Promise<void> {
  filesLoading.value = true
  filesError.value = ''
  try {
    files.value = await listRefFiles()
  } catch (err) {
    filesError.value = classifyApiError(err).message
  } finally {
    filesLoading.value = false
  }
}

// F33 上传：png/jpeg ≤10MB ≤4096px（服务端校验）；成功后只刷新文件列表。
async function onPickFile(e: Event): Promise<void> {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || uploading.value) return
  uploading.value = true
  filesError.value = ''
  try {
    const saved = await uploadRefFile(file)
    note.value = `已上传 ${saved.name}`
    files.value = await listRefFiles()
  } catch (err) {
    filesError.value = classifyApiError(err).message
  } finally {
    uploading.value = false
    input.value = ''
  }
}

function fmtSize(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + ' KB'
  return n + ' B'
}

function add(): void {
  const u = url.value.trim()
  if (!u) return
  draft.add({ image_id: imageId.value.trim() || `img-${draft.pending.length + 1}`, url: u })
  url.value = ''
  imageId.value = ''
  note.value = '已加入待下发列表'
}

function ackOf(st: RefsState): { ack: 'pending' | 'success' | 'failed'; ackError: string } {
  if (!st.ack) return { ack: 'pending', ackError: '' }
  return st.ack.success
    ? { ack: 'success', ackError: '' }
    : { ack: 'failed', ackError: st.ack.error || '' }
}

// 下发切换/移除：HTTP 已受理只记「已下发」，ack 成功才算「已应用」（§5.2）。
async function apply(op: 'switch' | 'remove', images: RefsImage[] = draft.pending): Promise<void> {
  if (busy.value) return
  busy.value = true
  stateError.value = ''
  note.value = ''
  const submitted = op === 'switch' ? [...images] : []
  try {
    state.value = op === 'switch' ? await switchRefs(submitted) : await removeRefs()
    const { ack, ackError } = ackOf(state.value)
    draft.pushSnapshot({ images: submitted, http: 'ok', httpError: '', ack, ackError, atMs: Date.now() })
    draft.clearDirty()
    note.value =
      op === 'switch'
        ? ack === 'success'
          ? `切换指令已下发并已应用（${submitted.length} 张）`
          : `切换指令已下发（${submitted.length} 张），等待 Vidu 回执`
        : ack === 'success'
          ? '移除指令已下发并已应用'
          : '移除指令已下发，等待 Vidu 回执'
  } catch (err) {
    const msg =
      err instanceof ApiError && err.status === 502 ? `参考图指令失败（type14）：${err.message}` : classifyApiError(err).message
    stateError.value = msg
    draft.pushSnapshot({ images: submitted, http: 'error', httpError: msg, ack: 'failed', ackError: msg, atMs: Date.now() })
    // 刷新失败状态但不清掉本次错误提示。
    await loadState().catch(() => undefined)
    stateError.value = msg
  } finally {
    busy.value = false
  }
}

// 重试 ack 失败的快照：重新下发同一组内容，就地更新回执状态。
async function retrySnapshot(index: number): Promise<void> {
  const snap = draft.snapshots[index]
  if (!snap || busy.value) return
  busy.value = true
  try {
    const st = await switchRefs(snap.images)
    state.value = st
    const { ack, ackError } = ackOf(st)
    draft.updateSnapshot(index, { http: 'ok', httpError: '', ack, ackError })
    note.value = ack === 'success' ? '重试已应用' : '重试指令已下发，等待 Vidu 回执'
  } catch (err) {
    const msg = classifyApiError(err).message
    draft.updateSnapshot(index, { http: 'error', httpError: msg, ack: 'failed', ackError: msg })
    stateError.value = msg
  } finally {
    busy.value = false
  }
}

// 撤销：恢复上一组已提交内容；显示「正在恢复 → 回执成功/失败」，失败保留快照可重试。
async function undo(): Promise<void> {
  const prev = draft.snapshots[1]
  if (!prev || restoring.value || busy.value) return
  restoring.value = true
  stateError.value = ''
  note.value = '正在恢复上一组参考图…'
  try {
    const st = await switchRefs(prev.images)
    state.value = st
    const { ack, ackError } = ackOf(st)
    draft.pushSnapshot({ images: [...prev.images], http: 'ok', httpError: '', ack, ackError, atMs: Date.now() })
    note.value = ack === 'success' ? '已恢复上一组参考图' : '恢复指令已下发，等待 Vidu 回执'
  } catch (err) {
    const msg = classifyApiError(err).message
    stateError.value = `恢复失败：${msg}`
    note.value = ''
  } finally {
    restoring.value = false
  }
}

function snapAckLabel(s: { ack: string; ackError: string }): string {
  if (s.ack === 'success') return 'Vidu 已应用'
  if (s.ack === 'failed') return `Vidu 回执失败${s.ackError ? `：${s.ackError}` : ''}`
  return '等待 Vidu 回执'
}

onMounted(() => {
  void loadState()
  void loadFiles()
})
</script>

<template>
  <div class="refs">
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <div class="bar">
      <span class="mono">已下发 {{ state?.images?.length ?? 0 }} 张 · {{ state?.source || 'fake' }}</span>
      <button type="button" class="mini" :disabled="stateLoading" @click="loadState">刷新</button>
    </div>
    <p v-if="stateLoading && !state" class="note">正在读取参考图状态…</p>
    <div v-else-if="stateError" class="errbox" role="alert">
      <span class="error">{{ stateError }}</span>
      <button type="button" class="mini" :disabled="stateLoading" @click="loadState">重试</button>
    </div>

    <p v-if="state?.ack" class="note" aria-live="polite" :class="{ error: !state.ack.success }">
      最近回执：{{ state.ack.success ? '成功（已应用）' : `失败 ${state.ack.error || ''}` }}
    </p>

    <!-- 已下发快照：HTTP 状态与 Vidu ack 分离（§5.2），最多保留 3 组。 -->
    <section v-if="draft.snapshots.length" class="snaps" aria-label="最近下发快照">
      <div class="bar">
        <span class="mono">最近下发 {{ draft.snapshots.length }} 组</span>
        <button v-if="draft.canUndo" type="button" class="mini" :disabled="restoring || busy" @click="undo">
          {{ restoring ? '正在恢复…' : '恢复上一组' }}
        </button>
      </div>
      <ul class="list">
        <li v-for="(snap, i) in draft.snapshots" :key="snap.atMs">
          <span class="mono">{{ snap.images.length }} 张 · HTTP {{ snap.http === 'ok' ? '已受理' : '失败' }}</span>
          <span class="note" :class="{ error: snap.ack === 'failed' }">{{ snapAckLabel(snap) }}</span>
          <button v-if="snap.ack === 'failed'" type="button" class="mini" :disabled="busy" @click="retrySnapshot(i)">重试</button>
        </li>
      </ul>
    </section>

    <!-- F33 本地 refs 文件：独立 loading/error/重试；404/500 不影响已下发状态与切图。 -->
    <section class="files" aria-label="本地参考图文件">
      <div class="bar">
        <span class="mono">本地文件 {{ files.length }} 个</span>
        <button type="button" class="mini" :disabled="uploading" @click="fileInput?.click()">
          {{ uploading ? '上传中…' : '上传图片' }}
        </button>
        <input ref="fileInput" type="file" accept=".png,.jpg,.jpeg" hidden @change="onPickFile" />
      </div>
      <p v-if="filesLoading && files.length === 0" class="note">正在读取文件列表…</p>
      <div v-else-if="filesError" class="errbox" role="alert">
        <span class="error">文件列表加载失败：{{ filesError }}</span>
        <button type="button" class="mini" :disabled="filesLoading" @click="loadFiles">重试</button>
      </div>
      <p v-else-if="files.length === 0" class="note">refs 目录暂无文件。上传 png/jpeg（≤10MB、≤4096px）供开播时参考。</p>
      <ul v-else class="list">
        <li v-for="f in files" :key="f.name">
          <span class="url">{{ f.name }}</span>
          <span class="mono">{{ fmtSize(f.size) }}</span>
        </li>
      </ul>
    </section>

    <p v-if="draft.pending.length === 0" class="note">暂无参考图。可粘贴图片 URL 或资产 ID 后下发。</p>
    <ul v-else class="list">
      <li v-for="(img, i) in draft.pending" :key="`${img.image_id}-${i}`">
        <span class="mono">{{ img.image_id }}</span>
        <span class="url">{{ img.url }}</span>
        <button type="button" class="mini" @click="draft.removeAt(i)">移除</button>
      </li>
    </ul>

    <div class="row">
      <input v-model="imageId" type="text" placeholder="资产 ID（可选）" />
      <input v-model="url" type="text" placeholder="图片 URL" @keyup.enter="add" />
      <button type="button" class="mini" :disabled="!url.trim()" @click="add">添加</button>
    </div>
    <div class="row btns">
      <button type="button" class="mini" :disabled="busy || draft.pending.length === 0" @click="apply('switch')">下发切换</button>
      <button type="button" class="mini" :disabled="busy" @click="apply('remove')">移除全部</button>
    </div>
    <p class="note">参考图以 URL/资产 ID 引用下发；本地文件仅作展示与人工核对（蓝本 stage 语义）。</p>
  </div>
</template>

<style scoped>
.refs {
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
.errbox {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 8px;
  border: 1px solid var(--nt-danger-border);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.snaps {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 6px 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.list li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.url {
  overflow: hidden;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.row {
  display: flex;
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
