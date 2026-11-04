import { apiRequest } from './client'
import type { LiveStatus, RestLiveEvent, RtcToken, RtcTokenWire, WireEvent } from './types'

function idem(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// 幂等键复用：按 sessionId+action 在 2s 时间片内复用同一键，
// 前端双击/重试产生的重复请求会被后端幂等层合并。
const IDEM_WINDOW_MS = 2000
const idemCache = new Map<string, { key: string; at: number }>()

export function idemFor(sessionId: string, action: string): string {
  const scope = `${sessionId || 'new'}:${action}`
  const now = Date.now()
  const hit = idemCache.get(scope)
  if (hit && now - hit.at < IDEM_WINDOW_MS) return hit.key
  const key = idem()
  idemCache.set(scope, { key, at: now })
  return key
}

export async function getLiveStatus(signal?: AbortSignal): Promise<LiveStatus> {
  return apiRequest<LiveStatus>('/api/v1/live/status', { signal })
}

export type LiveMode = 'local-preview' | 'public-live'
export type LiveCheckStatus = 'pass' | 'warn' | 'fail' | 'unknown' | 'running'

export interface LivePreflightCheck {
  code: string
  status: LiveCheckStatus
  blocking: boolean
  source: string
  target?: string
  detail?: string
  observedAtMs: number
}

export interface LivePreflightResult {
  checkId: string
  preflightToken: string
  personaId: string
  revision?: number
  ready: boolean
  mode: LiveMode
  draftId: string
  draftRevision: number
  inputRevisions: Record<string, number>
  resultDigest: string
  observedAtMs: number
  expiresAtMs: number
  checks: LivePreflightCheck[]
}

interface LivePreflightWire {
  check_id?: string
  preflight_token?: string
  persona_id?: string
  revision?: number | string
  ready?: boolean
  mode: LiveMode
  draft_id?: string
  draft_revision?: number | string
  input_revisions?: Record<string, number | string>
  result_digest?: string
  observed_at_ms?: number | string
  expires_at_ms?: number | string
  checks?: Array<{
    code: string
    status: LiveCheckStatus
    blocking?: boolean
    source?: string
    target?: string
    detail?: string
    observed_at_ms?: number | string
  }>
}

export async function preflightLive(input: {
  personaId?: string
  draftId?: string
  expectedRevision?: number
}): Promise<LivePreflightResult> {
  const r = await apiRequest<LivePreflightWire>('/api/v1/live/preflight', {
    method: 'POST',
    body: {
      persona_id: input.personaId ?? '',
      draft_id: input.draftId ?? '',
      expected_revision: input.expectedRevision ?? 0
    }
  })
  return {
    checkId: r.check_id ?? '',
    preflightToken: r.preflight_token ?? '',
    personaId: r.persona_id ?? '',
    revision: toOptionalNumber(r.revision),
    ready: Boolean(r.ready),
    mode: r.mode,
    draftId: r.draft_id ?? '',
    draftRevision: toNumber(r.draft_revision),
    inputRevisions: Object.fromEntries(Object.entries(r.input_revisions ?? {}).map(([k, v]) => [k, toNumber(v)])),
    resultDigest: r.result_digest ?? '',
    observedAtMs: toNumber(r.observed_at_ms),
    expiresAtMs: toNumber(r.expires_at_ms),
    checks: (r.checks ?? []).map((c) => ({
      code: c.code,
      status: c.status,
      blocking: Boolean(c.blocking),
      source: c.source ?? '',
      target: c.target,
      detail: c.detail,
      observedAtMs: toNumber(c.observed_at_ms)
    }))
  }
}

export interface StartLiveInput {
  personaId?: string
  avatarId?: string
  greeting?: string
  sessionId?: string
  draftId: string
  revision: number
  expectedRevision: number
  resultDigest: string
  preflightToken: string
}

export async function startLive(
  input: StartLiveInput
): Promise<{ sessionId: string; phase: string; liveId: string; mode: LiveMode }> {
  const { sessionId, ...rest } = input
  return apiRequest('/api/v1/live/start', {
    method: 'POST',
    body: { ...rest, idempotencyKey: idemFor(sessionId ?? '', 'live.start') }
  })
}

export async function stopLive(reason = 'manual'): Promise<{ phase: string; recordingState: string; ok: boolean; note: string }> {
  return apiRequest('/api/v1/live/stop', {
    method: 'POST',
    body: { reason, idempotencyKey: idem() }
  })
}

export async function say(text: string, mode: 'sentence' | 'whole' = 'sentence'): Promise<{ utteranceId: string }> {
  return apiRequest('/api/v1/live/say', {
    method: 'POST',
    body: { text, mode, source: 'stage', idempotencyKey: idem() }
  })
}

export async function sendCommand(text: string): Promise<{ accepted: boolean; reason: string }> {
  return apiRequest('/api/v1/live/command', {
    method: 'POST',
    body: { text, idempotencyKey: idem() }
  })
}

export async function interrupt(): Promise<{ ok: boolean }> {
  return apiRequest('/api/v1/live/interrupt', { method: 'POST', body: {} })
}

export async function clearTranscript(): Promise<{ ok: boolean; cleared: number }> {
  return apiRequest('/api/v1/live/transcript/clear', { method: 'POST', body: {} })
}

export async function stopActions(): Promise<{ ok: boolean; cleared: number }> {
  return apiRequest('/api/v1/live/actions/stop', { method: 'POST', body: {} })
}

export async function clearActions(): Promise<{ ok: boolean; cleared: number }> {
  return apiRequest('/api/v1/live/actions/clear', { method: 'POST', body: {} })
}

export async function resetLlm(sessionId: string): Promise<{ clearedTurns: number }> {
  return apiRequest('/api/v1/live/llm/reset', { method: 'POST', body: { sessionId } })
}

// 签发/刷新 RTC 凭据：未配置时 ok=false + mode=unconfigured，前端不得伪造入会。
// protojson：int64 字符串化、布尔/字段可能省略；这里统一成前端稳定类型。
export function normalizeRtcToken(r: RtcTokenWire): RtcToken {
  const mode = r.mode === 'managed' || r.mode === 'customer' ? r.mode : 'unconfigured'
  return {
    ok: Boolean(r.ok),
    mode,
    provider: r.provider ?? (mode === 'unconfigured' ? 'none' : ''),
    token: r.token ?? '',
    expiresAt: toNumber(r.expiresAt),
    roomId: r.roomId ?? '',
    userId: r.userId ?? '',
    reason: r.reason ?? ''
  }
}

export async function getRtcToken(sessionId: string): Promise<RtcToken> {
  const q = new URLSearchParams({ session_id: sessionId })
  const r = await apiRequest<RtcTokenWire>(`/api/v1/live/rtc-token?${q.toString()}`)
  return normalizeRtcToken(r)
}

function toOptionalNumber(v: number | string | undefined): number | undefined {
  if (v === undefined || v === '') return undefined
  const n = Number(v)
  return Number.isFinite(n) ? n : undefined
}

function toNumber(v: number | string | undefined, fallback = 0): number {
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : fallback
}

function decodePayload(base64?: string): Record<string, unknown> | undefined {
  if (!base64) return undefined
  try {
    const bin = atob(base64)
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    return JSON.parse(new TextDecoder().decode(bytes)) as Record<string, unknown>
  } catch {
    return undefined
  }
}

// REST 轮询与 WS 共用同一事件信封：补齐 int64 字符串化与 payload base64 解码。
export function normalizeRestEvent(e: RestLiveEvent): WireEvent {
  return {
    seq: toNumber(e.seq),
    event_id: e.eventId ?? '',
    session_id: e.sessionId ?? '',
    type: e.type,
    ts_ms: toNumber(e.tsMs),
    epoch: toNumber(e.epoch),
    request_id: e.requestId ?? '',
    payload: decodePayload(e.payloadJson)
  }
}

export async function listEvents(afterSeq: number, sessionId = '', limit = 200): Promise<WireEvent[]> {
  const q = new URLSearchParams({ after_seq: String(afterSeq), limit: String(limit) })
  if (sessionId) q.set('session_id', sessionId)
  const reply = await apiRequest<{ events?: RestLiveEvent[] }>(`/api/v1/live/events?${q.toString()}`)
  return (reply.events ?? []).map(normalizeRestEvent)
}
