import { apiRequest } from './client'

// 平台接入（推流 + 弹幕采集）后端 /api/v1/{stream,collector} 的唯一出口。
// stream_key 只写不回显：响应只携带 key_fingerprint8，前端不持久化、不展示明文。

function idem(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export type StreamState = 'idle' | 'starting' | 'pushing' | 'degraded' | 'rejected' | 'stopped'
export type CollectorState = 'off' | 'connecting' | 'collecting' | 'degraded' | 'stopped'

export interface StreamStatus {
  state: StreamState
  bitrateKbps: number
  fps: number
  droppedFrames: number
  keyFingerprint8: string
  lastError: string
  platform: string
}

interface StreamStatusWire {
  state?: StreamState
  bitrate_kbps?: number
  fps?: number
  dropped_frames?: number
  key_fingerprint8?: string
  last_error?: string
  platform?: string
}

export interface CollectorStatus {
  state: CollectorState
  roomId: string
  lastMsgMs: number
  msgsPerMin: number
}

interface CollectorStatusWire {
  state?: CollectorState
  room_id?: string
  last_msg_ms?: number
  msgs_per_min?: number
}

function num(v: unknown, fallback = 0): number {
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : fallback
}

export async function getStreamStatus(signal?: AbortSignal): Promise<StreamStatus> {
  const r = await apiRequest<StreamStatusWire>('/api/v1/stream/status', { signal })
  return {
    state: r.state ?? 'idle',
    bitrateKbps: num(r.bitrate_kbps),
    fps: num(r.fps),
    droppedFrames: num(r.dropped_frames),
    keyFingerprint8: r.key_fingerprint8 ?? '',
    lastError: r.last_error ?? '',
    platform: r.platform ?? ''
  }
}

// 保存推流目标：stream_key 随请求一次性上送，成功后由调用方清空本地输入。
export async function saveStreamTarget(rtmpUrl: string, streamKey: string): Promise<{ ok: boolean; keyFingerprint8: string }> {
  const r = await apiRequest<{ ok?: boolean; key_fingerprint8?: string }>('/api/v1/stream/target', {
    method: 'POST',
    body: { platform: 'douyin', rtmp_url: rtmpUrl, stream_key: streamKey, idempotency_key: idem() }
  })
  return { ok: Boolean(r.ok), keyFingerprint8: r.key_fingerprint8 ?? '' }
}

export async function startStream(): Promise<{ ok: boolean; reason: string }> {
  const r = await apiRequest<{ ok?: boolean; reason?: string }>('/api/v1/stream/start', {
    method: 'POST',
    body: { idempotency_key: idem() }
  })
  return { ok: Boolean(r.ok), reason: r.reason ?? '' }
}

export async function stopStream(): Promise<{ ok: boolean }> {
  const r = await apiRequest<{ ok?: boolean }>('/api/v1/stream/stop', {
    method: 'POST',
    body: { idempotency_key: idem() }
  })
  return { ok: Boolean(r.ok) }
}

export async function getCollectorStatus(signal?: AbortSignal): Promise<CollectorStatus> {
  const r = await apiRequest<CollectorStatusWire>('/api/v1/collector/status', { signal })
  return {
    state: r.state ?? 'off',
    roomId: r.room_id ?? '',
    lastMsgMs: num(r.last_msg_ms),
    msgsPerMin: num(r.msgs_per_min)
  }
}

// relay_mode 固定 external：弹幕经外部中转采集，房号即抖音直播间号。
export async function saveCollectorConfig(roomId: string): Promise<{ ok: boolean }> {
  const r = await apiRequest<{ ok?: boolean }>('/api/v1/collector/config', {
    method: 'POST',
    body: { platform: 'douyin', room_id: roomId, relay_mode: 'external', idempotency_key: idem() }
  })
  return { ok: Boolean(r.ok) }
}
