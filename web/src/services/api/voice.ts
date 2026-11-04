import { apiRequest } from './client'
import type { VoiceStatus } from '../voice/types'

export interface VoiceTicket {
  ticket: string
  purpose: 'voice'
  expires_at: number
  ttl_s: number
}

export function getVoiceStatus(): Promise<VoiceStatus> {
  return apiRequest<VoiceStatus>('/api/v1/voice/status')
}

export function createVoiceTicket(sessionId: string, clientId: string): Promise<VoiceTicket> {
  const query = `session_id=${encodeURIComponent(sessionId)}&client_id=${encodeURIComponent(clientId)}`
  return apiRequest<VoiceTicket>(`/api/v1/voice/ticket?${query}`, { method: 'POST', body: {} })
}
