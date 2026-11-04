// 地址唯一出口（R-UI-61）：组件/store 不拼 URL，CSP 只允许 self。
export const API_BASE = ''

export function apiPath(path: string): string {
  return `${API_BASE}${path}`
}

export function wsEventsUrl(afterSeq: number, sessionId = ''): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const sid = sessionId ? `&session_id=${encodeURIComponent(sessionId)}` : ''
  return `${proto}//${window.location.host}${apiPath(`/ws/v1/events?after_seq=${afterSeq}${sid}`)}`
}

export function wsVoiceUrl(sessionId: string, clientId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const query = `session_id=${encodeURIComponent(sessionId)}&client_id=${encodeURIComponent(clientId)}`
  return `${proto}//${window.location.host}${apiPath(`/ws/v1/voice?${query}`)}`
}

export function wsRtcAudioUrl(sessionId: string, epoch: number): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const query = `session_id=${encodeURIComponent(sessionId)}&epoch=${epoch}`
  return `${proto}//${window.location.host}${apiPath(`/ws/v1/rtc-audio?${query}`)}`
}
