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

export interface RtcAdapter {
  join(creds: RtcCreds, video: HTMLVideoElement): Promise<void>
  leave(): Promise<void>
  mute(muted: boolean): void
  destroy(): void
}

export interface RtcAdapterFactory {
  create(provider: 'artc' | 'trtc'): RtcAdapter
}
