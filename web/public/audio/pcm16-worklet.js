// 采集链路内重采样：任意 AudioContext 采样率 → 16kHz/mono/s16le/20ms。
// 只通过当前回合内存传输 PCM；此处理器不写日志、不持久化音频。
class PCM16WorkletProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    this.ratio = sampleRate / 16000
    this.position = 0
    this.input = []
    this.pending = []
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0]
    if (!channel || !channel.length) return true
    for (let i = 0; i < channel.length; i += 1) this.input.push(channel[i])

    while (this.position + 1 < this.input.length) {
      const index = Math.floor(this.position)
      const fraction = this.position - index
      const sample = this.input[index] + (this.input[index + 1] - this.input[index]) * fraction
      const clamped = Math.max(-1, Math.min(1, sample))
      this.pending.push(clamped < 0 ? Math.round(clamped * 32768) : Math.round(clamped * 32767))
      this.position += this.ratio
    }

    const consumed = Math.floor(this.position)
    if (consumed > 0) {
      this.input = this.input.slice(consumed)
      this.position -= consumed
    }

    while (this.pending.length >= 320) {
      const frame = new Int16Array(this.pending.splice(0, 320))
      this.port.postMessage(frame, [frame.buffer])
    }
    return true
  }
}

registerProcessor('pcm16-worklet', PCM16WorkletProcessor)
