export type VoiceCapabilitySource = 'real' | 'fake' | 'unknown' | 'unconfigured'

export interface VoiceCapability {
  source: VoiceCapabilitySource
  ready: boolean
  device?: string
  model?: string
  error_code?: string
  message?: string
}

export interface VoiceStatus {
  ready: boolean
  asr: VoiceCapability
  llm: VoiceCapability
  tts: VoiceCapability
  live: VoiceCapability
  default_mode: 'ptt' | 'continuous'
  continuous: boolean
  audio_retained: boolean
  interrupt_songs: boolean
  max_audio_ms: number
}

export type VoiceMode = 'ptt' | 'continuous'
export type VoicePhase = 'idle' | 'connecting' | 'listening' | 'thinking' | 'speaking' | 'error'

export interface VoiceTimings {
  speech_end_at_ms?: number
  asr_final_at_ms?: number
  llm_first_token_at_ms?: number
  tts_first_audio_at_ms?: number
  speech_end_to_first_audio_ms?: number
}

export interface VoiceServerEvent {
  type: string
  stage?: string
  turn_id?: string
  revision?: number
  text?: string
  reason?: string
  error?: string
  error_code?: string
  message?: string
  retryable?: boolean
  timings?: VoiceTimings
}

export interface VoiceTransportOptions {
  onEvent: (event: VoiceServerEvent) => void
  onClose?: (intentional: boolean) => void
}

export interface VoiceTransport {
  connect(sessionId: string, clientId: string): Promise<void>
  begin(epoch: number, mode: VoiceMode, clientTurnId: string): void
  sendPCM(pcm: Int16Array): void
  end(): void
  cancel(): void
  close(): void
}

export interface VoiceCapture {
  start(onFrame: (pcm: Int16Array) => void): Promise<void>
  stop(): Promise<void>
}
