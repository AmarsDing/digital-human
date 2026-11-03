import { ApiError, apiRequest } from './client'

// M3 面板后端（/api/v1/{tts,actions,choreo,koubo,prompt,songs,sessions,cost,danmaku}）的
// 唯一出口。blob/上传统一收敛进 apiRequest 的 blob/form 模式，
// 鉴权、10s 超时与错误归类保持单点（client.ts）。

export interface MediaFile {
  name: string
  size: number
  mtime: number
  lib: string
}

export interface Voice {
  name: string
  label: string
  kind?: string
}

export interface VoiceInfo {
  current: string
  active_base: string
  voices: Voice[]
}

export interface TTSQueue {
  depth: number
  max: number
  active: number
  calls: number
  updated_at_ms: number
}

export interface ActionLibrary {
  id: string
  name: string
  description: string
  created_at_ms: number
  action_count?: number
}

export interface ActionItem {
  id: string
  library_id: string
  name: string
  instruction: string
  created_at_ms: number
}

export interface ActionStatus {
  queue: string[]
  last_text: string
  last_at_ms: number
  fired: number
  merged: number
  dropped: number
  deferred: number
  queue_max: number
}

export interface ActionResult {
  ok: boolean
  deferred: boolean
  dropped?: string
  vip?: boolean
  reason?: string
  gap_s?: number
  queued?: number
  priority: number
}

export interface ChoreoStep {
  cmd: string
  gap_s?: number
}

export interface ChoreoGroup {
  id: string
  name: string
  order?: string
  steps: ChoreoStep[]
}

export interface ChoreoConfig {
  enabled: boolean
  step_gap_s: number
  group_gap_s: number
  step_order: string
  groups: ChoreoGroup[]
}

export interface ChoreoStatus {
  running: boolean
  enabled: boolean
  group: string
  step: number
  runs: number
  paused_until_ms: number
  last_fire_at_ms: number
}

export interface RefsImage {
  image_id: string
  url: string
  kind?: string
}

export interface RefsAck {
  success: boolean
  error?: string
  at_ms: number
}

export interface RefsState {
  images: RefsImage[]
  last_op?: string
  last_op_at_ms?: number
  ack?: RefsAck
  source: string
}

export interface KouboTake {
  file: string
  dur: number
  ts: number
}

export interface KouboSegment {
  id: string
  text: string
  takes: KouboTake[]
  selected: number | null
  action: string
  status: string
}

export interface KouboJob {
  running: boolean
  done: number
  total: number
  idx: string | null
  stop: boolean
  error?: string
}

export interface KouboState {
  text: string
  max_chars: number
  segments: KouboSegment[]
  playing: boolean
  play_idx: string | null
  stop: boolean
  updated: number
  tts_job: KouboJob
}

export interface SongState {
  file: string
  script: string
  song_playing: boolean
  epoch: number
  started_at_ms?: number
  caption_lines: string[]
  lyrics_source: string
  decoder: string
}

export interface SessionRecord {
  id: string
  live_id?: string
  phase: string
  mode: string
  persona_id?: string
  avatar_id?: string
  started_at_ms: number
  ended_at_ms: number
  duration_s: number
  recording_state: string
  error_code?: string
  updated_at_ms: number
}

export interface Ledger {
  session_id: string
  account_id: string
  credits_start: number
  credits_end: number
  credits_spent: number
  tokens_in: number
  tokens_out: number
  tokens_total: number
  llm_calls: number
  diamonds: number
  gift_count: number
  estimated_cost: number
  cost_source?: string
  started_at_ms?: number
  ended_at_ms?: number
  updated_at_ms: number
  source: string
}

export interface Totals {
  key: string
  sessions: number
  credits_spent: number
  tokens_in: number
  tokens_out: number
  tokens_total: number
  llm_calls: number
  diamonds: number
  gift_count: number
  estimated_cost: number
  estimated: boolean
}

export interface SessionSummary extends SessionRecord {
  cost?: Ledger
}

export interface SessionDetail {
  session: SessionRecord
  cost?: Ledger
  events: WireLiteEvent[]
  event_count: number
}

export interface WireLiteEvent {
  seq: number
  event_id: string
  session_id: string
  type: string
  ts_ms: number
  payload?: Record<string, unknown>
}

export interface DanmakuState {
  mode: string
  enabled: boolean
  top: { text: string; count: number }[]
  pending?: unknown[]
  recent?: string[]
}

// ---------- 通用 blob / multipart（收敛进 apiRequest） ----------

async function apiBlob(path: string, body?: unknown): Promise<Blob> {
  return apiRequest<Blob>(path, {
    method: 'POST',
    body: body === undefined ? {} : body,
    responseType: 'blob'
  })
}

async function apiUpload(path: string, file: File): Promise<{ files: MediaFile[]; count: number }> {
  const form = new FormData()
  form.append('file', file, file.name)
  return apiRequest<{ files: MediaFile[]; count: number }>(path, { method: 'POST', form })
}

// ---------- TTS ----------

export async function getVoices(signal?: AbortSignal): Promise<VoiceInfo> {
  const r = await apiRequest<VoiceInfo>('/api/v1/tts/voices', { signal })
  return { current: r.current ?? '', active_base: r.active_base ?? '', voices: r.voices ?? [] }
}

export async function setVoice(voice: string): Promise<void> {
  await apiRequest('/api/v1/tts/voice', { method: 'POST', body: { voice } })
}

export async function previewVoice(text: string, voice = ''): Promise<Blob> {
  return apiBlob('/api/v1/tts/preview', { text, voice })
}

export async function listTTSFiles(): Promise<{ dir: string; files: MediaFile[]; count: number }> {
  return apiRequest('/api/v1/tts/files')
}

export async function uploadTTSFile(file: File): Promise<{ files: MediaFile[]; count: number }> {
  return apiUpload('/api/v1/tts/upload', file)
}

export async function importTTSFolder(path: string): Promise<{ imported: number; skipped: number; dir: string }> {
  return apiRequest('/api/v1/tts/import-folder', { method: 'POST', body: { path } })
}

export async function deleteTTSFile(name: string): Promise<void> {
  await apiRequest('/api/v1/tts/delete', { method: 'POST', body: { file_name: name } })
}

export async function getTTSQueue(): Promise<TTSQueue> {
  const r = await apiRequest<{ queue: TTSQueue }>('/api/v1/tts/queue')
  return r.queue
}

// ---------- 动作库 / 调度 ----------

export async function listActionLibraries(): Promise<ActionLibrary[]> {
  const r = await apiRequest<{ libraries: ActionLibrary[] }>('/api/v1/actions/libraries')
  return r.libraries ?? []
}

export async function createActionLibrary(name: string, description = ''): Promise<ActionLibrary> {
  const r = await apiRequest<{ library: ActionLibrary }>('/api/v1/actions/libraries', { method: 'POST', body: { name, description } })
  return r.library
}

export async function deleteActionLibrary(id: string): Promise<void> {
  await apiRequest(`/api/v1/actions/libraries/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function listActions(libraryId = ''): Promise<ActionItem[]> {
  const q = libraryId ? `?library_id=${encodeURIComponent(libraryId)}` : ''
  const r = await apiRequest<{ actions: ActionItem[] }>(`/api/v1/actions${q}`)
  return r.actions ?? []
}

export async function createAction(libraryId: string, name: string, instruction = ''): Promise<ActionItem> {
  const r = await apiRequest<{ action: ActionItem }>('/api/v1/actions', {
    method: 'POST',
    body: { library_id: libraryId, name, instruction }
  })
  return r.action
}

export async function deleteAction(id: string): Promise<void> {
  await apiRequest(`/api/v1/actions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function getActionQueue(): Promise<ActionStatus> {
  const r = await apiRequest<{ status: ActionStatus }>('/api/v1/actions/queue')
  return r.status
}

export async function sendAction(text: string, source = 'manual'): Promise<ActionResult> {
  const r = await apiRequest<{ result: ActionResult }>('/api/v1/actions/send', { method: 'POST', body: { text, source } })
  return r.result
}

// ---------- 编排 ----------

export async function getChoreo(): Promise<ChoreoConfig> {
  const r = await apiRequest<{ config: ChoreoConfig }>('/api/v1/choreo')
  return r.config
}

export async function saveChoreo(cfg: ChoreoConfig): Promise<ChoreoConfig> {
  const r = await apiRequest<{ config: ChoreoConfig }>('/api/v1/choreo', { method: 'POST', body: cfg })
  return r.config
}

export async function upsertChoreoGroup(group: ChoreoGroup): Promise<ChoreoConfig> {
  const r = await apiRequest<{ config: ChoreoConfig }>('/api/v1/choreo/groups', { method: 'POST', body: group })
  return r.config
}

export async function deleteChoreoGroup(id: string): Promise<ChoreoConfig> {
  const r = await apiRequest<{ config: ChoreoConfig }>(`/api/v1/choreo/groups/${encodeURIComponent(id)}`, { method: 'DELETE' })
  return r.config
}

export async function startChoreo(): Promise<{ started: boolean; status: ChoreoStatus }> {
  return apiRequest('/api/v1/choreo/start', { method: 'POST', body: {} })
}

export async function stopChoreo(): Promise<{ status: ChoreoStatus }> {
  return apiRequest('/api/v1/choreo/stop', { method: 'POST', body: {} })
}

export async function runChoreoOnce(): Promise<{ steps: number; status: ChoreoStatus }> {
  return apiRequest('/api/v1/choreo/run-once', { method: 'POST', body: {} })
}

export async function getChoreoStatus(): Promise<ChoreoStatus> {
  const r = await apiRequest<{ status: ChoreoStatus }>('/api/v1/choreo/status')
  return r.status
}

// ---------- 口播 ----------

export async function getKouboState(): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/state')
  return r.state
}

export async function splitKoubo(text: string, maxChars = 120): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/split', { method: 'POST', body: { text, max_chars: maxChars } })
  return r.state
}

export async function updateKouboSegment(id: string, text: string): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/seg', { method: 'POST', body: { id, text } })
  return r.state
}

export async function mergeKouboSegments(ids: string[]): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/merge', { method: 'POST', body: { ids } })
  return r.state
}

export async function splitKouboSegment(id: string, pos: number): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/seg/split', { method: 'POST', body: { id, pos } })
  return r.state
}

export async function kouboTTS(id: string): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/tts', { method: 'POST', body: { id } })
  return r.state
}

export async function kouboTTSAll(onlyMissing = true): Promise<{ job: KouboJob; state?: KouboState }> {
  return apiRequest('/api/v1/koubo/tts_all', { method: 'POST', body: { only_missing: onlyMissing } })
}

export async function kouboSelect(id: string, take: number): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/select', { method: 'POST', body: { id, take } })
  return r.state
}

export async function kouboAction(id: string): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/action', { method: 'POST', body: { id } })
  return r.state
}

export async function kouboActionsAll(): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/actions_all', { method: 'POST', body: {} })
  return r.state
}

export async function playKoubo(id: string): Promise<void> {
  await apiRequest('/api/v1/koubo/play', { method: 'POST', body: { id } })
}

export async function playAllKoubo(gapMs = 0): Promise<void> {
  await apiRequest('/api/v1/koubo/play_all', { method: 'POST', body: { gap_ms: gapMs } })
}

export async function stopKoubo(): Promise<KouboState> {
  const r = await apiRequest<{ state: KouboState }>('/api/v1/koubo/stop', { method: 'POST', body: {} })
  return r.state
}

export async function kouboAudio(name: string): Promise<Blob> {
  return apiRequest<Blob>(`/api/v1/koubo/audio/${encodeURIComponent(name)}`, { responseType: 'blob' })
}

// ---------- 参考图 ----------

export async function getRefs(): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt')
  return r.state
}

export async function switchRefs(images: RefsImage[]): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt', { method: 'POST', body: { op_type: 'switch', images } })
  return r.state
}

export async function removeRefs(): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt', { method: 'POST', body: { op_type: 'remove' } })
  return r.state
}

// 上报 Vidu type14（prompt 下发失败等）以恢复参考图状态；后端只记录并回传最新 state。
export async function notePromptType14(error: string): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt/type14', { method: 'POST', body: { error } })
  return r.state
}

// ---------- 歌曲 ----------

export async function listSongs(): Promise<MediaFile[]> {
  const r = await apiRequest<{ files: MediaFile[] }>('/api/v1/songs')
  return r.files ?? []
}

export async function uploadSong(file: File): Promise<{ files: MediaFile[]; count: number }> {
  return apiUpload('/api/v1/songs/upload', file)
}

export async function playSong(fileName: string, script = ''): Promise<SongState> {
  const r = await apiRequest<{ state: SongState }>('/api/v1/songs/play', { method: 'POST', body: { file_name: fileName, script } })
  return r.state
}

export async function stopSong(): Promise<SongState> {
  const r = await apiRequest<{ state: SongState }>('/api/v1/songs/stop', { method: 'POST', body: {} })
  return r.state
}

export async function deleteSong(name: string): Promise<void> {
  await apiRequest(`/api/v1/songs/${encodeURIComponent(name)}`, { method: 'DELETE' })
}

export async function getSongStatus(): Promise<SongState> {
  const r = await apiRequest<{ state: SongState }>('/api/v1/songs/status')
  return r.state
}

// ---------- 复盘 / 成本 / 弹幕 ----------

export async function listSessions(limit = 20, offset = 0, phase = ''): Promise<SessionSummary[]> {
  const q = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (phase) q.set('phase', phase)
  const r = await apiRequest<{ sessions: SessionSummary[] }>(`/api/v1/sessions?${q.toString()}`)
  return r.sessions ?? []
}

export async function getSession(id: string, includeSummary = false): Promise<SessionDetail> {
  const q = includeSummary ? '?include=summary' : ''
  return apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}${q}`)
}

export async function exportSession(id: string, format: 'json' | 'csv' = 'json'): Promise<Blob> {
  return apiRequest<Blob>(`/api/v1/sessions/${encodeURIComponent(id)}/export?format=${format}`, { responseType: 'blob' })
}

export async function getLedger(sessionId: string): Promise<Ledger | null> {
  try {
    const r = await apiRequest<{ ledger: Ledger }>(`/api/v1/cost/ledger/${encodeURIComponent(sessionId)}`)
    return r.ledger
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null
    throw err
  }
}

export async function costSummary(groupBy: 'session' | 'account' = 'session'): Promise<{ group_by: string; totals: Totals[] }> {
  return apiRequest(`/api/v1/cost/summary?group_by=${groupBy}`)
}

// 写入/更新成本台账（内部计量链路使用，也可用于人工校正）。
export async function recordLedger(ledger: Ledger): Promise<Ledger | null> {
  const r = await apiRequest<{ ledger: Ledger }>('/api/v1/cost/ledger', { method: 'POST', body: ledger })
  return r.ledger
}

// ---------- 弹幕 ----------

export interface DanmakuPendingItem {
  id: string
  userName: string
  text: string
  createdAtMs: number
}

export async function getDanmakuState(): Promise<{ state: DanmakuState; top_scope: string }> {
  return apiRequest('/api/v1/danmaku/state')
}

export async function listDanmakuPending(): Promise<DanmakuPendingItem[]> {
  const r = await apiRequest<{ items: DanmakuPendingItem[] }>('/api/v1/danmaku/pending')
  return (r.items ?? []).map((i) => ({
    id: i.id ?? '',
    userName: i.userName ?? '',
    text: i.text ?? '',
    createdAtMs: i.createdAtMs ?? 0
  }))
}

export async function setDanmakuMode(mode: 'off' | 'confirm' | 'auto'): Promise<{ changed: boolean; mode: string }> {
  return apiRequest('/api/v1/danmaku/mode', { method: 'POST', body: { mode } })
}

export async function confirmDanmaku(pendingId: string): Promise<{ ok: boolean; reason: string }> {
  return apiRequest('/api/v1/danmaku/confirm', { method: 'POST', body: { pending_id: pendingId } })
}

export async function dismissDanmaku(pendingId: string): Promise<{ ok: boolean }> {
  return apiRequest('/api/v1/danmaku/dismiss', { method: 'POST', body: { pending_id: pendingId } })
}
