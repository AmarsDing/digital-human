import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import RtcPlayer from '../RtcPlayer.vue'
import '../adapter'
import type { RtcAdapter, RtcCreds, RtcRemoteAudio } from '../types'

const harness = vi.hoisted(() => ({
  connect: vi.fn(), send: vi.fn(), close: vi.fn(), start: vi.fn(), stop: vi.fn()
}))
vi.mock('../RtcAudioSocket', () => ({
  RtcAudioSocket: class {
    connect = harness.connect
    send = harness.send
    close = harness.close
  }
}))
vi.mock('../RemoteAudioCapture', async (importOriginal) => {
  const original = await importOriginal<typeof import('../RemoteAudioCapture')>()
  return {
    ...original,
    RemoteAudioCapture: class {
      start = harness.start
      stop = harness.stop
    }
  }
})

function creds(provider: 'artc' | 'trtc' = 'artc'): RtcCreds {
  return { mode: 'managed', provider, appId: 'a', roomId: 'dh-s-1', userId: 'u', token: 't', tokenExpireAt: 0 }
}
async function flush(): Promise<void> {
  await nextTick()
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

describe('RtcPlayer 公开远端音频出口', () => {
  let audioHandler: ((audio: RtcRemoteAudio) => void) | null
  beforeEach(() => {
    vi.clearAllMocks()
    audioHandler = null
    const adapter: RtcAdapter = {
      join: vi.fn(async () => {}), leave: vi.fn(async () => {}), mute: vi.fn(), destroy: vi.fn(),
      onRemoteAudio: (handler) => { audioHandler = handler }
    }
    window.__DH_RTC_FACTORY__ = { create: () => adapter }
    harness.connect.mockResolvedValue(undefined)
    harness.start.mockResolvedValue(undefined)
    harness.stop.mockResolvedValue(undefined)
  })
  afterEach(() => { delete window.__DH_RTC_FACTORY__ })

  it('仅公开采集配置启动 WS，并消费 adapter 的远端 MediaStream', async () => {
    const getUserMedia = vi.fn()
    Object.defineProperty(navigator, 'mediaDevices', { value: { getUserMedia }, configurable: true })
    const wrapper = mount(RtcPlayer, {
      props: { creds: creds(), audioCapture: { sessionId: 's-1', epoch: 7 } }
    })
    await flush()
    expect(harness.connect).toHaveBeenCalledWith('s-1', 7)
    const stream = { getAudioTracks: () => [{}] } as unknown as MediaStream
    const handler = audioHandler as ((audio: RtcRemoteAudio) => void) | null
    expect(handler).not.toBeNull()
    handler?.({ kind: 'stream', stream })
    await flush()
    expect(harness.start).toHaveBeenCalledWith(stream, expect.any(Function))
    expect(getUserMedia).not.toHaveBeenCalled()
    await wrapper.unmount()
    expect(harness.stop).toHaveBeenCalled()
    expect(harness.close).toHaveBeenCalled()
  })

  it('TRTC SDK Float32 远端 PCM 经统一切帧后发送', async () => {
    mount(RtcPlayer, {
      props: { creds: creds('trtc'), audioCapture: { sessionId: 's-1', epoch: 2 } }
    })
    await flush()
    const handler = audioHandler as ((audio: RtcRemoteAudio) => void) | null
    handler?.({ kind: 'samples', samples: new Float32Array(960).fill(0.5) })
    expect(harness.send).toHaveBeenCalledWith({ sequence: 0, pcm: expect.any(Int16Array) })
    expect(harness.start).not.toHaveBeenCalled()
  })

  it('操作员页面没有 audioCapture 时不创建公开音频连接', async () => {
    mount(RtcPlayer, { props: { creds: creds() } })
    await flush()
    expect(harness.connect).not.toHaveBeenCalled()
  })
})
