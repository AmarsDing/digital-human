import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'

interface Frame { sequence: number; pcm: Int16Array }
function loadProcessor(rate: number) {
  const path = resolve(process.cwd(), 'public/audio/rtc-capture-worklet.js')
  expect(existsSync(path), '公开 RTC 音频必须使用独立 48kHz worklet').toBe(true)
  const source = readFileSync(path, 'utf8')
  let Processor: new () => { process(inputs: Float32Array[][]): boolean; port: { postMessage: ReturnType<typeof vi.fn> } }
  class AudioWorkletProcessor { port = { postMessage: vi.fn() } }
  runInNewContext(source, {
    sampleRate: rate,
    AudioWorkletProcessor,
    registerProcessor: (_name: string, ctor: typeof Processor) => { Processor = ctor },
    Int16Array,
    Float32Array,
    Math
  })
  return new Processor!()
}

describe('rtc-capture AudioWorklet', () => {
  it('输出 48kHz/mono/s16le 20ms 帧和单调 sequence', () => {
    const processor = loadProcessor(48000)
    processor.process([[new Float32Array(960).fill(0.5)]])
    processor.process([[new Float32Array(960).fill(-0.5)]])
    const calls = processor.port.postMessage.mock.calls
    expect(calls).toHaveLength(2)
    expect(calls[0][0].sequence).toBe(0)
    expect(calls[1][0].sequence).toBe(1)
    for (const call of calls) {
      const frame = call[0] as Frame
      expect(frame.pcm.length).toBe(960)
      expect(frame.pcm.byteLength).toBe(1920)
    }
  })

  it('跨 128-sample 块缓冲且 Float32 饱和转换', () => {
    const processor = loadProcessor(48000)
    for (let i = 0; i < 15; i += 1) {
      const block = new Float32Array(128).fill(0.25)
      if (i === 0) {
        block[0] = 2
        block[1] = -2
      }
      processor.process([[block]])
    }
    expect(processor.port.postMessage).toHaveBeenCalledTimes(2)
    const frame = processor.port.postMessage.mock.calls[0][0] as Frame
    expect(frame.pcm[0]).toBe(32767)
    expect(frame.pcm[1]).toBe(-32768)
  })

  it('44.1kHz 输入重采样后仍严格输出 960 samples', () => {
    const processor = loadProcessor(44100)
    for (let i = 0; i < 20; i += 1) processor.process([[new Float32Array(128).fill(0.25)]])
    const frame = processor.port.postMessage.mock.calls[0][0] as Frame
    expect(frame.pcm.length).toBe(960)
  })
})
