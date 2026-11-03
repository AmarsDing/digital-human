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
  // M5 A20：详情页人设展示；deleted=true 表示档案已软删（空态）。
  persona?: PersonaBrief
}

export interface PersonaBrief {
  id: string
  name?: string
  deleted?: boolean
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

// API 边界归一化（v2 §5.1）：服务端可能下发 images:null，前端一律按数组处理，避免模板渲染崩。
export function normalizeRefsState(state: RefsState): RefsState {
  return { ...state, images: Array.isArray(state?.images) ? state.images : [] }
}

export async function getRefs(): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt')
  return normalizeRefsState(r.state)
}

export async function switchRefs(images: RefsImage[]): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt', { method: 'POST', body: { op_type: 'switch', images } })
  return normalizeRefsState(r.state)
}

export async function removeRefs(): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt', { method: 'POST', body: { op_type: 'remove' } })
  return normalizeRefsState(r.state)
}

// 上报 Vidu type14（prompt 下发失败等）以恢复参考图状态；后端只记录并回传最新 state。
export async function notePromptType14(error: string): Promise<RefsState> {
  const r = await apiRequest<{ state: RefsState }>('/api/v1/prompt/type14', { method: 'POST', body: { error } })
  return normalizeRefsState(r.state)
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

// ---------- 人设（F38/G04） ----------

export interface Persona {
  id: string
  slug?: string
  name: string
  tagline?: string
  voice_id?: string
  avatar_note?: string
  system_prompt?: string
  is_active?: boolean
  updated_at_ms?: number
}

export interface PersonaList {
  active: string
  items: Persona[]
  prompt: string
  prompt_chars: number
  storage: 'memory' | 'ent'
}

export interface PersonaSaveBody {
  id: string
  name?: string
  tagline?: string
  voice?: string
  avatar_note?: string
  system_prompt: string
}

// 列表带当前存储标识（无 PG 时 memory，重启即丢）；prompt 为当前人设完整提示词。
export async function listPersonas(signal?: AbortSignal): Promise<PersonaList> {
  const r = await apiRequest<PersonaList>('/api/v1/personas', { signal })
  return {
    active: r.active ?? '',
    items: r.items ?? [],
    prompt: r.prompt ?? '',
    prompt_chars: r.prompt_chars ?? 0,
    storage: r.storage ?? 'memory'
  }
}

// 切换立即生效：后端同步清当前场次 LLM 记忆，返回真实清除轮数。
export async function selectPersona(id: string): Promise<{ active: string; name: string; chars: number; cleared_turns: number }> {
  return apiRequest('/api/v1/personas/select', { method: 'POST', body: { id } })
}

export async function savePersona(body: PersonaSaveBody): Promise<Persona> {
  const r = await apiRequest<{ persona: Persona }>('/api/v1/personas/save', { method: 'POST', body })
  return r.persona
}

export async function deletePersona(id: string): Promise<void> {
  await apiRequest('/api/v1/personas/delete', { method: 'POST', body: { id } })
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

// ---------- 录播（F34/N2，蓝本 bridge.py:408-434 /api/record） ----------

export interface RecordStatus {
  ok: boolean
  enabled: boolean
  /** none | recording | processing | ready | failed | unavailable */
  state: string
  url?: string
  live_id?: string
  note?: string
}

export async function getRecord(): Promise<RecordStatus> {
  return apiRequest('/api/v1/record')
}

// setRecord 保存录播开关；只在建会话时生效（蓝本 note 语义）。
export async function setRecord(enabled: boolean): Promise<RecordStatus> {
  return apiRequest('/api/v1/record', { method: 'POST', body: { enabled } })
}

// fetchRecord 手动再拉一场出片；不传 liveId 用最近一场。未开通/dev → 503 recording_unavailable。
export async function fetchRecord(liveId = ''): Promise<{ ok: boolean; live_id: string; state: string }> {
  return apiRequest('/api/v1/record/fetch', { method: 'POST', body: { live_id: liveId } })
}

// ---------- 额度（F08/F36/G03/N3，蓝本 bridge.py:1508 /api/credits） ----------

export interface CreditsStatus {
  ok: boolean
  /** 无 Vidu 凭据/未装配时为 false，前端显示明确空态。 */
  configured: boolean
  remain?: number | null
  remain_credit?: number
  total_credit?: number
  [k: string]: unknown
}

export async function getCredits(): Promise<CreditsStatus> {
  return apiRequest('/api/v1/credits')
}

// ---------- 对话调试（N1，蓝本 bridge.py:469 /api/chat） ----------

// sendChat 管理员与数字人对话：走 LLM（独立 admin 记忆 + 人设），应答经 TTS 上屏播报；
// 未开播 409 not_live、LLM 未配置 503 llm_not_configured（fail-closed）。
// 流式 LLM + 逐句 TTS 可能远超 10s 默认超时，放宽到 120s。
export async function sendChat(text: string): Promise<{ ok: boolean; reply: string }> {
  return apiRequest('/api/v1/chat', { method: 'POST', body: { text }, timeoutMs: 120000 })
}

// ---------- 贵宾名单（F29/G24，蓝本 bridge.py:878-940 五路由） ----------

export interface PatronEntry {
  nick: string
  uid?: string
  alias?: string
  note?: string
  since?: string
}

export interface PatronsState {
  live: boolean
  session_id: string
  count: number
  welcomed: number
  saved_cnt: number
}

export interface PatronsListResp {
  ok: boolean
  items: PatronEntry[]
  state: PatronsState
}

export async function getPatrons(signal?: AbortSignal): Promise<PatronsListResp> {
  return apiRequest('/api/v1/patrons', { signal })
}

export async function getPatronsIDs(): Promise<{ nicks: string[]; uids: string[] }> {
  return apiRequest('/api/v1/patrons/ids')
}

export async function savePatrons(items: PatronEntry[]): Promise<{ ok: boolean; count: number }> {
  return apiRequest('/api/v1/patrons/save', { method: 'POST', body: { items } })
}

export async function patronJoin(nick: string, uid = '', note = ''): Promise<{ ok: boolean; line?: string; skipped?: string }> {
  return apiRequest('/api/v1/patron/join', { method: 'POST', body: { nick, uid, note } })
}

export async function patronWelcome(nick: string, note = ''): Promise<{ ok: boolean; line?: string; skipped?: string }> {
  return apiRequest('/api/v1/patron/welcome', { method: 'POST', body: { nick, note } })
}

// ---------- 参考图本地文件（F33，蓝本 bridge.py:1348 /api/refs） ----------

export interface RefFile {
  name: string
  size: number
}

// listRefFiles 扫描服务端 refs 目录（.jpg/.jpeg/.png/.webp）。
export async function listRefFiles(): Promise<RefFile[]> {
  const r = await apiRequest<{ ok: boolean; items: RefFile[] }>('/api/v1/refs')
  return r.items ?? []
}

// uploadRefFile multipart 上传（png/jpeg ≤10MB ≤4096px；校验失败 400 稳定错误码）。
export async function uploadRefFile(file: File): Promise<RefFile> {
  const fd = new FormData()
  fd.append('file', file, file.name)
  const r = await apiRequest<{ ok: boolean; file: RefFile }>('/api/v1/refs/upload', {
    method: 'POST',
    body: fd,
    timeoutMs: 30000
  })
  return r.file
}

// ---------- Vidu 多 key（F42/G18，蓝本 bridge.py:1458,1476 /api/vidu/keys） ----------

export interface ViduKeyEntry {
  tail8: string
  is_active: boolean
  added_at_ms: number
}

export interface ViduKeysState {
  ok: boolean
  active: string
  keys: ViduKeyEntry[]
}

export async function getViduKeys(): Promise<ViduKeysState> {
  return apiRequest('/api/v1/vidu/keys')
}

// viduKeysAction 蓝本单端点动作分发：add（key 明文 + activate）/ activate / delete（按尾 8 位）。
// 删除当前生效 key → 409 cannot_delete_active_key；切换后既有资产失效仅 UI 提示（v3 H6）。
export async function viduKeysAction(action: 'add' | 'activate' | 'delete', key: string, activate = false): Promise<ViduKeysState> {
  return apiRequest('/api/v1/vidu/keys', { method: 'POST', body: { action, key, activate } })
}

// ---------- 数字人档案（G 批 M2，v2 §3.1） ----------

export interface ViduSyncState {
  state: string
  stage?: string
  error?: string
  job_id?: string
  updated_at_ms?: number
}

export interface AvatarListItem {
  persona_id: string
  slug: string
  name: string
  vidu_avatar_id?: string
  vidu_image_uri?: string
  expires_at_ms?: number
  vidu_sync_state: ViduSyncState
  is_active: boolean
  // 列表项同样下发（与 detail 同键名），列表页置 active 预检依赖该字段。
  avatar_asset_id?: string
  updated_at_ms?: number
  revision: number
}

export interface AvatarRefItem {
  asset_id: string
  ord: number
  url: string
  vidu_uri?: string
  sha256?: string
}

export interface AvatarDetail extends AvatarListItem {
  system_prompt: string
  greeting: string
  voice_id: string
  action_library_id?: string
  avatar_asset_id?: string
  refs: AvatarRefItem[]
}

export async function listAvatars(
  page = 1,
  size = 20,
  includeDeleted = false,
  signal?: AbortSignal
): Promise<{ avatars: AvatarListItem[]; total: number; storage: string }> {
  return apiRequest(
    `/api/v1/avatars?page=${page}&size=${size}${includeDeleted ? '&include_deleted=true' : ''}`,
    { signal }
  )
}

export async function getAvatar(id: string, signal?: AbortSignal): Promise<AvatarDetail> {
  const r = await apiRequest<{ avatar: AvatarDetail }>(`/api/v1/avatars/${encodeURIComponent(id)}`, { signal })
  return r.avatar
}

export interface AvatarUpsertInput {
  name: string
  slug?: string
  system_prompt?: string
  greeting?: string
  voice_id?: string
  action_library_id?: string
  image_asset_id?: string
}

export async function createAvatar(input: AvatarUpsertInput): Promise<AvatarDetail> {
  const r = await apiRequest<{ avatar: AvatarDetail }>('/api/v1/avatars', { method: 'POST', body: input })
  return r.avatar
}

export async function updateAvatar(id: string, input: AvatarUpsertInput, revision: number): Promise<AvatarDetail> {
  const r = await apiRequest<{ avatar: AvatarDetail }>(`/api/v1/avatars/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: { ...input, revision }
  })
  return r.avatar
}

export async function deleteAvatar(id: string): Promise<void> {
  await apiRequest(`/api/v1/avatars/${encodeURIComponent(id)}/delete`, { method: 'POST' })
}

export async function duplicateAvatar(id: string): Promise<AvatarDetail> {
  const r = await apiRequest<{ avatar: AvatarDetail }>(`/api/v1/avatars/${encodeURIComponent(id)}/duplicate`, {
    method: 'POST'
  })
  return r.avatar
}

export async function activateAvatar(id: string): Promise<void> {
  await apiRequest(`/api/v1/avatars/${encodeURIComponent(id)}/activate`, { method: 'POST' })
}

export async function syncAvatarToVidu(id: string): Promise<{ ok: boolean }> {
  return apiRequest(`/api/v1/avatars/${encodeURIComponent(id)}/sync-vidu`, { method: 'POST' })
}

export interface AvatarUploadResult {
  asset_id: string
  sha256: string
  dedup: boolean
}

export async function uploadAvatarImage(file: File, signal?: AbortSignal): Promise<AvatarUploadResult> {
  const form = new FormData()
  form.append('file', file)
  return apiRequest('/api/v1/avatars/upload', { method: 'POST', form, signal, timeoutMs: 60000 })
}

export async function uploadAvatarRef(personaId: string, file: File, signal?: AbortSignal): Promise<AvatarUploadResult> {
  const form = new FormData()
  form.append('file', file)
  return apiRequest(`/api/v1/avatars/${encodeURIComponent(personaId)}/refs`, {
    method: 'POST',
    form,
    signal,
    timeoutMs: 60000
  })
}

export async function removeAvatarRef(personaId: string, assetId: string): Promise<void> {
  await apiRequest(`/api/v1/avatars/${encodeURIComponent(personaId)}/refs/remove`, {
    method: 'POST',
    body: { asset_id: assetId }
  })
}

// 图片读取走 fetch→blob→objectURL（<img> 无法带 Authorization 头，v3 §5.1/§9.2）；
// URL 由后端 detail.refs[].url 给出，未给出时按路由形状拼接。
export function avatarImageUrl(personaId: string): string {
  return `/api/v1/avatars/${encodeURIComponent(personaId)}/image`
}

export async function fetchAvatarImage(personaId: string, signal?: AbortSignal): Promise<Blob> {
  return apiRequest<Blob>(avatarImageUrl(personaId), { responseType: 'blob', signal })
}

export function avatarRefImageUrl(personaId: string, assetId: string): string {
  return `/api/v1/avatars/${encodeURIComponent(personaId)}/assets/${encodeURIComponent(assetId)}/image`
}

export async function fetchAvatarRefImage(personaId: string, assetId: string, signal?: AbortSignal): Promise<Blob> {
  return apiRequest<Blob>(avatarRefImageUrl(personaId, assetId), { responseType: 'blob', signal })
}
