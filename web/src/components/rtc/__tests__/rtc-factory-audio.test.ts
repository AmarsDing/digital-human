import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'
import type { RtcAdapter, RtcCreds, RtcRemoteAudio } from '../types'

function loadFactory(window: unknown): void {
  runInNewContext(readFileSync(resolve(process.cwd(), 'public/vendor/rtc-factory.js'), 'utf8'), {
    window, Promise, Error, Number, setTimeout, clearTimeout, Float32Array
  })
}
function creds(provider: 'artc' | 'trtc'): RtcCreds {
  return { mode: 'managed', provider, appId: '1', roomId: 'r', userId: 'u', token: 't', tokenExpireAt: 0 }
}

describe('RTC vendor 远端音轨出口', () => {
  it('ARTC 只暴露 video.srcObject 中真实存在的远端音轨', async () => {
    const handlers: Record<string, (uid: string) => void> = {}
    const engine = {
      on: (name: string, cb: (uid: string) => void) => { handlers[name] = cb },
      joinChannel: vi.fn(async () => {}), leaveChannel: vi.fn(async () => {}),
      setRemoteViewConfig: vi.fn(), setChannelProfile: vi.fn(async () => {}),
      setDefaultPublishLocalAudioStream: vi.fn(async () => {}),
      setDefaultPublishLocalVideoStream: vi.fn(async () => {}),
      setDefaultSubscribeAllRemoteAudioStreams: vi.fn(async () => {}),
      setDefaultSubscribeAllRemoteVideoStreams: vi.fn(async () => {})
    }
    const window = {
      AliRtcEngine: { getInstance: () => engine, isSupported: async () => ({ support: true }), setLogLevel: vi.fn() }
    }
    loadFactory(window)
    const adapter = (window as unknown as { __DH_RTC_FACTORY__: { create(): RtcAdapter } }).__DH_RTC_FACTORY__.create()
    const onAudio = vi.fn()
    adapter.onRemoteAudio(onAudio)
    const stream = { getAudioTracks: () => [{}] } as unknown as MediaStream
    const video = { srcObject: stream, play: () => Promise.resolve() } as unknown as HTMLVideoElement
    await adapter.join(creds('artc'), video)
    handlers.videoSubscribeStateChanged('remote-vidu')
    expect(onAudio).toHaveBeenCalledWith({ kind: 'stream', stream })
  })

  it('TRTC 显式订阅远端音频并转发 SDK Float32 PCM', async () => {
    const handlers: Record<string, (event: { userId: string; data?: Float32Array }) => void> = {}
    const client = {
      on: (name: string, cb: (event: { userId: string; data?: Float32Array }) => void) => { handlers[name] = cb },
      enterRoom: vi.fn(async () => {}), startRemoteVideo: vi.fn(async () => {}),
      startRemoteAudio: vi.fn(async () => {}), callExperimentalAPI: vi.fn(async () => {}),
      exitRoom: vi.fn(async () => {}), destroy: vi.fn()
    }
    const window = {
      TRTC: {
        create: () => client,
        EVENT: { REMOTE_VIDEO_AVAILABLE: 'video', REMOTE_AUDIO_AVAILABLE: 'audio', AUDIO_FRAME: 'pcm' }
      }
    }
    loadFactory(window)
    const adapter = (window as unknown as { __DH_RTC_FACTORY__: { create(provider: 'trtc'): RtcAdapter } }).__DH_RTC_FACTORY__.create('trtc')
    const onAudio = vi.fn<(audio: RtcRemoteAudio) => void>()
    adapter.onRemoteAudio(onAudio)
    const video = { play: () => Promise.resolve() } as unknown as HTMLVideoElement
    await adapter.join(creds('trtc'), video)
    await handlers.audio({ userId: 'remote-vidu' })
    expect(client.startRemoteAudio).toHaveBeenCalledWith({ userId: 'remote-vidu' })
    expect(client.callExperimentalAPI).toHaveBeenCalledWith('enableAudioFrameEvent', {
      enable: true, userId: 'remote-vidu', sampleRate: 48000, channelCount: 1
    })
    const samples = new Float32Array(128)
    handlers.pcm({ userId: 'remote-vidu', data: samples })
    expect(onAudio).toHaveBeenCalledWith({ kind: 'samples', samples })
  })
})
