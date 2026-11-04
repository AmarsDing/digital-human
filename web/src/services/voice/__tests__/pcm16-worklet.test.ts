import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'

function loadProcessor(sampleRate: number) {
  const source = readFileSync(resolve(process.cwd(), 'public/audio/pcm16-worklet.js'), 'utf8')
  let Processor: new () => { process(inputs: Float32Array[][]): boolean; port: { postMessage: ReturnType<typeof vi.fn> } }
  class AudioWorkletProcessor {
    port = { postMessage: vi.fn() }
  }
  runInNewContext(source, {
    sampleRate,
    AudioWorkletProcessor,
    registerProcessor: (_name: string, ctor: typeof Processor) => { Processor = ctor },
    Int16Array,
    Float32Array,
    Math
  })
  return new Processor!()
}

describe('pcm16 AudioWorklet', () => {
  it('48kHz 输入重采样为 16kHz mono s16le 20ms 帧', () => {
    const processor = loadProcessor(48000)
    processor.process([[new Float32Array(960).fill(0.5)]])
    expect(processor.port.postMessage).toHaveBeenCalledTimes(1)
    const frame = processor.port.postMessage.mock.calls[0][0] as Int16Array
    expect(frame.length).toBe(320)
    expect(frame.byteLength).toBe(640)
    expect(frame[0]).toBeGreaterThan(16000)
    expect(frame[0]).toBeLessThan(17000)
  })

  it('跨 128-sample process 块保持连续并只输出完整帧', () => {
    const processor = loadProcessor(48000)
    for (let i = 0; i < 15; i += 1) processor.process([[new Float32Array(128).fill(0.25)]])
    expect(processor.port.postMessage).toHaveBeenCalledTimes(2)
    for (const call of processor.port.postMessage.mock.calls) expect((call[0] as Int16Array).length).toBe(320)
  })

  it('44.1kHz 输入同样输出精确 20ms 帧', () => {
    const processor = loadProcessor(44100)
    processor.process([[new Float32Array(900).fill(-0.5)]])
    const frame = processor.port.postMessage.mock.calls[0][0] as Int16Array
    expect(frame.length).toBe(320)
    expect(frame[0]).toBeLessThan(-16000)
    expect(frame[0]).toBeGreaterThan(-17000)
  })
})
