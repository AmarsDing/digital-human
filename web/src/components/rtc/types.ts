export interface RtcCreds {
  mode: 'managed' | 'customer'
  provider: 'artc' | 'trtc'
  appId: string
  roomId: string
  userId: string
  token: string
  tokenExpireAt: number
}

export type RtcState = 'unconfigured' | 'joining' | 'joined' | 'failed'

export type RtcRemoteAudio =
  | { kind: 'stream'; stream: MediaStream }
  | { kind: 'samples'; samples: Float32Array }

export interface RtcAudioCaptureConfig {
  sessionId: string
  epoch: number
}

export interface RtcAdapter {
  join(creds: RtcCreds, video: HTMLVideoElement): Promise<void>
  leave(): Promise<void>
  mute(muted: boolean): void
  destroy(): void
  onRemoteAudio(handler: (audio: RtcRemoteAudio) => void): void
}

export interface RtcAdapterFactory {
  create(provider: 'artc' | 'trtc'): RtcAdapter
}
