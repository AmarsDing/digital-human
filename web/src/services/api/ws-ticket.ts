import { apiRequest } from './client'

export interface WsTicket {
  ticket: string
  expiresAt: number
  ttlS: number
}

export async function createWsTicket(sessionId = ''): Promise<WsTicket> {
  const q = sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : ''
  return apiRequest<WsTicket>(`/api/v1/ws-ticket${q}`, { method: 'POST', body: {} })
}
