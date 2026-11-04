import { afterEach, describe, expect, it, vi } from 'vitest'
import { Float32PCMEncoder, RemoteAudioCapture } from '../RemoteAudioCapture'

afterEach(() => vi.unstubAllGlobals())

describe('RemoteAudioCapture', () => {
  it('只连接传入的 RTC 远端 MediaStream，不请求麦克风', async () => {
    const getUserMedia = vi.fn()
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } })
    const source = { connect: vi.fn(), disconnect: vi.fn() }
    const node = { port: { onmessage: null as ((event: MessageEvent) => void) | null }, connect: vi.fn(), disconnect: vi.fn() }
    const sink = { gain: { value: 1 }, connect: vi.fn(), disconnect: vi.fn() }
    const context = {
      state: 'running', resume: vi.fn().mockResolvedValue(undefined),
      audioWorklet: { addModule: vi.fn().mockResolvedValue(undefined) },
      createMediaStreamSource: vi.fn(() => source), createGain: vi.fn(() => sink),
      destination: {}, close: vi.fn().mockResolvedValue(undefined)
    }
    vi.stubGlobal('AudioContext', vi.fn(() => context))
    vi.stubGlobal('AudioWorkletNode', vi.fn(() => node))
    const stream = { getAudioTracks: () => [{}] } as unknown as MediaStream
    const capture = new RemoteAudioCapture()
    const onFrame = vi.fn()
    await capture.start(stream, onFrame)

    expect(getUserMedia).not.toHaveBeenCalled()
    expect(context.createMediaStreamSource).toHaveBeenCalledWith(stream)
    expect(context.audioWorklet.addModule).toHaveBeenCalledWith('/audio/rtc-capture-worklet.js')
    const frame = { sequence: 4, pcm: new Int16Array(960) }
    node.port.onmessage?.({ data: frame } as MessageEvent)
    expect(onFrame).toHaveBeenCalledWith(frame)
    expect(sink.gain.value).toBe(0)
    await capture.stop()
  })

  it('TRTC Float32 PCM 出口同样切为 960 samples 并饱和转换', () => {
    const encoder = new Float32PCMEncoder()
    const frames = encoder.push(new Float32Array(960).fill(2))
    expect(frames).toHaveLength(1)
    expect(frames[0].sequence).toBe(0)
    expect(frames[0].pcm.length).toBe(960)
    expect(frames[0].pcm[0]).toBe(32767)
    expect(encoder.push(new Float32Array(960).fill(-2))[0].sequence).toBe(1)
  })
})
