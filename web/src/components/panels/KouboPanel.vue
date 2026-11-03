<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtSelect from '../ui/NtSelect.vue'
import {
  getKouboState,
  kouboAction,
  kouboActionsAll,
  kouboAudio,
  kouboSelect,
  kouboTTS,
  kouboTTSAll,
  mergeKouboSegments,
  playAllKoubo,
  playKoubo,
  splitKoubo,
  splitKouboSegment,
  stopKoubo,
  updateKouboSegment,
  type KouboSegment,
  type KouboState
} from '../../services/api/panels'

const state = ref<KouboState | null>(null)
const draft = ref('')
const maxChars = ref(120)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const note = ref('')
const picked = ref<string[]>([])
const editors = new Map<string, HTMLTextAreaElement>()
const editErr = ref<Record<string, string>>({})
let audio: HTMLAudioElement | null = null
let audioUrl = ''
let timer: number | undefined

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    state.value = await getKouboState()
    if (!draft.value && state.value.text) draft.value = state.value.text
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function run(fn: () => Promise<KouboState | void>, msg = ''): Promise<void> {
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    const st = await fn()
    if (st) state.value = st
    if (msg) note.value = msg
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

async function split(): Promise<void> {
  if (!draft.value.trim()) return
  await run(async () => {
    const st = await splitKoubo(draft.value.trim(), Math.max(10, maxChars.value))
    note.value = `已拆分为 ${st.segments.length} 段`
    return st
  })
}

async function synth(seg: KouboSegment): Promise<void> {
  await run(async () => kouboTTS(seg.id), `第 ${segIndex(seg) + 1} 段已合成`)
}

async function selectTake(seg: KouboSegment, idx: number): Promise<void> {
  await run(async () => kouboSelect(seg.id, idx), '已切换 take')
}

async function action(seg: KouboSegment): Promise<void> {
  await run(async () => kouboAction(seg.id), '动作指令已发送')
}

function togglePick(id: string): void {
  const i = picked.value.indexOf(id)
  if (i >= 0) picked.value.splice(i, 1)
  else picked.value.push(id)
}

// 合并选中段（至少 2 段相邻/连续），供口播整段起播。
async function mergePicked(): Promise<void> {
  if (picked.value.length < 2) return
  await run(async () => {
    const st = await mergeKouboSegments([...picked.value])
    picked.value = []
    note.value = `已合并为 ${st.segments.length} 段`
    return st
  })
}

// 按文本光标位置拆分当前段；未聚焦时取中点。
async function splitAt(seg: KouboSegment): Promise<void> {
  const el = editors.get(seg.id)
  const pos = el ? el.selectionStart : Math.floor(seg.text.length / 2)
  await run(async () => {
    const st = await splitKouboSegment(seg.id, pos)
    note.value = `已按第 ${pos} 字拆分为 ${st.segments.length} 段`
    return st
  })
}

function bindEditor(id: string, el: unknown): void {
  if (el instanceof HTMLTextAreaElement) editors.set(id, el)
  else editors.delete(id)
}

// 对已选 take 的全部段批量下发动作指令。
async function actionsAll(): Promise<void> {
  await run(async () => {
    const st = await kouboActionsAll()
    note.value = `已对 ${st.segments.length} 段下发动作`
    return st
  })
}

async function play(seg: KouboSegment): Promise<void> {
  await run(async () => {
    const take = seg.takes[seg.selected ?? seg.takes.length - 1]
    if (!take) throw new Error('该段还没有音频，请先合成')
    const blob = await kouboAudio(take.file)
    stopAudio()
    audioUrl = URL.createObjectURL(blob)
    audio = new Audio(audioUrl)
    await audio.play()
  })
  const st = await getKouboState().catch(() => null)
  if (st) state.value = st
}

async function ttsAll(): Promise<void> {
  await run(async () => {
    const r = await kouboTTSAll(true)
    state.value = await getKouboState()
    note.value = r.job.running ? `正在合成 0/${r.job.total}` : '合成任务已提交'
  })
  startPolling()
}

async function playAll(): Promise<void> {
  await run(async () => {
    await playAllKoubo(0)
    note.value = '开始整篇播放'
  })
  startPolling()
}

async function stop(): Promise<void> {
  stopAudio()
  await run(async () => stopKoubo(), '已停止')
}

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

async function refresh(): Promise<void> {
  const st = await getKouboState().catch(() => null)
  if (st) state.value = st
}

function startPolling(): void {
  if (timer) return
  timer = window.setInterval(async () => {
    const st = await getKouboState().catch(() => null)
    if (st) state.value = st
    if (st && !st.tts_job.running && !st.playing) {
      window.clearInterval(timer)
      timer = undefined
    }
  }, 1500)
}

function segIndex(seg: KouboSegment): number {
  return state.value ? state.value.segments.findIndex((s) => s.id === seg.id) : 0
}

// 分段状态中文映射，原码放 title。
const SEG_STATUS: Record<string, string> = {
  pending: '待合成',
  synthesizing: '合成中',
  done: '已合成',
  failed: '失败'
}
function segStatus(s: string): string {
  return SEG_STATUS[s] ?? s
}

// 分段文本失焦保存：失败时回滚为服务端值并给行内提示。
async function edit(seg: KouboSegment, ev: Event): Promise<void> {
  const el = ev.target as HTMLTextAreaElement
  const text = el.value
  if (text === seg.text) return
  const next = { ...editErr.value }
  delete next[seg.id]
  editErr.value = next
  try {
    const st = await updateKouboSegment(seg.id, text)
    state.value = st
    note.value = '已保存文本'
  } catch (err) {
    el.value = seg.text // 回滚为服务端值
    editErr.value = { ...editErr.value, [seg.id]: classifyApiError(err).message }
  }
}

onMounted(load)
onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
  stopAudio()
})
</script>

<template>
  <div class="koubo">
    <p v-if="loading" class="note">正在读取口播状态…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <label class="row">
      <span class="mono">字数上限</span>
      <input v-model.number="maxChars" type="number" min="10" max="2000" />
    </label>
    <textarea v-model="draft" rows="4" placeholder="输入口播文本，按标点拆分…" />
    <div class="row btns">
      <button type="button" class="mini" :disabled="busy || !draft.trim()" @click="split">拆分</button>
      <button
        type="button"
        class="mini"
        :disabled="busy || picked.length < 2"
        :title="picked.length < 2 ? '至少勾选 2 段才能合并' : '合并勾选的分段'"
        @click="mergePicked"
      >
        合并选中（{{ picked.length }}）
      </button>
      <button type="button" class="mini" :disabled="busy || !state" @click="ttsAll">合成全部</button>
      <button type="button" class="mini" :disabled="busy || !state" @click="actionsAll">全部动作</button>
      <button type="button" class="mini" :disabled="busy || !state" @click="playAll">整篇播放</button>
      <button type="button" class="mini" :disabled="busy || !state" @click="stop">停止</button>
      <button type="button" class="mini" :disabled="busy || !state" @click="refresh">刷新</button>
    </div>

    <p v-if="state?.tts_job.running" class="note">
      合成中 {{ state.tts_job.done }}/{{ state.tts_job.total }} …
    </p>
    <p v-else-if="state?.playing" class="ok">播放中…</p>

    <p v-if="state && state.segments.length === 0" class="note">还没有分段。输入文本后点击“拆分”。</p>
    <ul v-else class="segs">
      <li v-for="(seg, i) in state?.segments ?? []" :key="seg.id" class="seg">
        <div class="seg__head">
          <label class="pick mono">
            <input type="checkbox" :checked="picked.includes(seg.id)" @change="togglePick(seg.id)" />
            选中
          </label>
          <span class="mono" :title="seg.status">#{{ i + 1 }} · {{ seg.takes.length }} takes · {{ segStatus(seg.status) }}</span>
          <span class="mono">{{ seg.selected === null ? '未选 take' : `take ${seg.selected + 1}` }}</span>
        </div>
        <textarea :ref="(el) => bindEditor(seg.id, el)" :value="seg.text" rows="2" @change="edit(seg, $event)" />
        <p v-if="editErr[seg.id]" class="error" role="alert">保存失败，已回滚：{{ editErr[seg.id] }}</p>
        <div class="row btns">
          <button type="button" class="mini" :disabled="busy" @click="synth(seg)">合成</button>
          <button type="button" class="mini" :disabled="busy || seg.takes.length === 0" @click="play(seg)">试听</button>
          <button type="button" class="mini" :disabled="busy || seg.takes.length === 0" @click="action(seg)">动作</button>
          <button
            type="button"
            class="mini"
            :disabled="busy || seg.text.length < 2"
            title="按文本框光标位置拆分该段"
            @click="splitAt(seg)"
          >
            拆分
          </button>
          <NtSelect
            v-if="seg.takes.length > 1"
            label="take"
            :model-value="String(seg.selected ?? 0)"
            :options="seg.takes.map((t, ti) => ({ label: `take ${ti + 1} (${t.dur}s)`, value: String(ti) }))"
            @update:model-value="selectTake(seg, Number($event))"
          />
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.koubo {
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
.row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.row input {
  width: 80px;
  height: 30px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
textarea {
  width: 100%;
  box-sizing: border-box;
  padding: 6px 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  font-size: var(--nt-fs-caption);
  resize: vertical;
}
textarea:focus,
.row input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
}
.mini,
select {
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
.segs {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.seg {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.seg__head {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.pick {
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>
