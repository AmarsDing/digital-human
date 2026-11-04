// 仅处理 RTC adapter 提供的远端音轨：任意 AudioContext 采样率 → 48kHz/mono/s16le/20ms。
// sequence 在每帧边界递增；音频不写日志、不落盘，也不采集麦克风或系统输出。
class RTCCaptureWorkletProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    this.ratio = sampleRate / 48000
    this.position = 0
    this.input = []
    this.pending = []
    this.sequence = 0
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0]
    if (!channel || !channel.length) return true
    for (let i = 0; i < channel.length; i += 1) this.input.push(channel[i])

    while (this.position < this.input.length) {
      const index = Math.floor(this.position)
      const fraction = this.position - index
      if (index + 1 >= this.input.length && fraction > 0) break
      const next = this.input[Math.min(index + 1, this.input.length - 1)]
      const sample = this.input[index] + (next - this.input[index]) * fraction
      const clamped = Math.max(-1, Math.min(1, sample))
      this.pending.push(clamped < 0 ? Math.round(clamped * 32768) : Math.round(clamped * 32767))
      this.position += this.ratio
    }

    const consumed = Math.floor(this.position)
    if (consumed > 0) {
      this.input = this.input.slice(consumed)
      this.position -= consumed
    }

    while (this.pending.length >= 960) {
      const pcm = new Int16Array(this.pending.splice(0, 960))
      const frame = { sequence: this.sequence, pcm }
      this.sequence = (this.sequence + 1) >>> 0
      this.port.postMessage(frame, [pcm.buffer])
    }
    return true
  }
}

registerProcessor('rtc-capture-worklet', RTCCaptureWorkletProcessor)
