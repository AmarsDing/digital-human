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

export async function startLive(input: {
  personaId?: string
  avatarId?: string
  greeting?: string
  sessionId?: string
}): Promise<{ sessionId: string; phase: string; liveId: string }> {
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
