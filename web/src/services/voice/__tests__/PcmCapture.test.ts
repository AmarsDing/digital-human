import { afterEach, describe, expect, it, vi } from 'vitest'
import { PcmCapture } from '../PcmCapture'

afterEach(() => vi.unstubAllGlobals())

describe('PcmCapture', () => {
  it('授权未决时停止采集会立即关闭迟到流，不创建音频图', async () => {
    let resolveStream: (stream: MediaStream) => void = () => undefined
    const getUserMedia = vi.fn(() => new Promise<MediaStream>((resolve) => {
      resolveStream = resolve
    }))
    const stopTrack = vi.fn()
    const audioContext = vi.fn(() => ({
      state: 'running',
      resume: vi.fn().mockResolvedValue(undefined),
      audioWorklet: { addModule: vi.fn().mockResolvedValue(undefined) },
      createMediaStreamSource: vi.fn(() => ({ connect: vi.fn(), disconnect: vi.fn() })),
      createGain: vi.fn(() => ({ gain: { value: 1 }, connect: vi.fn(), disconnect: vi.fn() })),
      destination: {},
      close: vi.fn().mockResolvedValue(undefined)
    }))
    vi.stubGlobal('window', { isSecureContext: true })
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } })
    vi.stubGlobal('AudioContext', audioContext)
    vi.stubGlobal('AudioWorkletNode', vi.fn(() => ({
      port: { onmessage: null }, connect: vi.fn(), disconnect: vi.fn()
    })))

    const capture = new PcmCapture()
    const starting = capture.start(vi.fn())
    await vi.waitFor(() => expect(getUserMedia).toHaveBeenCalled())
    const rejection = expect(starting).rejects.toMatchObject({ name: 'AbortError' })
    await capture.stop()
    resolveStream({ getTracks: () => [{ stop: stopTrack }] } as unknown as MediaStream)
    await rejection

    expect(stopTrack).toHaveBeenCalledTimes(1)
    expect(audioContext).not.toHaveBeenCalled()
  })
})
