// 与 api/live/v1/live.proto、pkg/eventbus/eventbus.go 对齐的前端类型。
export interface FeedStatus {
  epoch: number
  lagMs: number
  owner: boolean
  ownerKind: string
}

export interface WsStatus {
  state: string
  reconnectTotal: number
  lastPongMs: number
}

export interface DanmakuStatus {
  pending: number
  droppedTotal: number
  t2SkippedTotal: number
}

export interface RtcStatus {
  mode: string
  provider: string
  tokenExpireAt: number
}

// GET /api/v1/live/rtc-token 走 protojson：int64 为字符串、布尔/字段缺省可能省略。
export interface RtcTokenWire {
  mode?: string
  provider?: string
  token?: string
  expiresAt?: number | string
  roomId?: string
  userId?: string
  ok?: boolean
  reason?: string
}

export interface RtcToken {
  ok: boolean
  mode: 'managed' | 'unconfigured' | 'customer'
  provider: string
  token: string
  expiresAt: number
  roomId: string
  userId: string
  reason: string
}

export interface LiveStatus {
  sessionId: string
  phase: string
  liveId: string
  startedAtMs: number
  durationS: number
  feed: FeedStatus
  ws: WsStatus
  danmaku: DanmakuStatus
  rtc: RtcStatus
  eventSeq: number
  transcripts: string[]
  errorCode: string
  errorMessage: string
}

export interface WireEvent {
  seq: number
  event_id: string
  session_id: string
  type: string
  ts_ms: number
  epoch: number
  request_id?: string
  payload?: Record<string, unknown>
}

// GET /api/v1/live/events 走 protojson：int64 输出为字符串、bytes 输出 base64。
export interface RestLiveEvent {
  seq: number | string
  eventId?: string
  sessionId?: string
  type: string
  tsMs?: number | string
  epoch?: number | string
  requestId?: string
  payloadJson?: string
}

export interface SettingGroupWire {
  id: string
  title: string
  // proto bytes → protojson 输出 base64 字符串
  valuesJson: string
  effective: 'immediate' | 'next_session'
}

export interface SettingsReply {
  groups: SettingGroupWire[]
  revision: number
}

export interface SaveSettingsReply {
  saved: boolean
  revision: number
  reloadRequired: string[]
  rejected: string[]
}
