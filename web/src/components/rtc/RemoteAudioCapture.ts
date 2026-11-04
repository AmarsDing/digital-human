export interface RTCPCMFrame {
  sequence: number
  pcm: Int16Array
}

/** 只接 RTC 远端 MediaStream；静音 sink 仅维持音频图，不引入麦克风或系统回录。 */
export class RemoteAudioCapture {
  private context: AudioContext | null = null
  private source: MediaStreamAudioSourceNode | null = null
  private node: AudioWorkletNode | null = null
  private sink: GainNode | null = null

  async start(stream: MediaStream, onFrame: (frame: RTCPCMFrame) => void): Promise<void> {
    if (this.context) return
    if (!stream.getAudioTracks().length) throw new Error('RTC 远端 MediaStream 不含音轨')
    const context = new AudioContext({ sampleRate: 48000 })
    this.context = context
    try {
      await context.resume()
      await context.audioWorklet.addModule('/audio/rtc-capture-worklet.js')
      this.source = context.createMediaStreamSource(stream)
      this.node = new AudioWorkletNode(context, 'rtc-capture-worklet')
      this.sink = context.createGain()
      this.sink.gain.value = 0
      this.node.port.onmessage = (event: MessageEvent<RTCPCMFrame>) => {
        if (event.data?.pcm instanceof Int16Array && event.data.pcm.length === 960) onFrame(event.data)
      }
      this.source.connect(this.node)
      this.node.connect(this.sink)
      this.sink.connect(context.destination)
    } catch (error) {
      await this.stop()
      throw error
    }
  }

  async stop(): Promise<void> {
    if (this.node) this.node.port.onmessage = null
    this.source?.disconnect()
    this.node?.disconnect()
    this.sink?.disconnect()
    const context = this.context
    this.context = null
    this.source = null
    this.node = null
    this.sink = null
    if (context && context.state !== 'closed') await context.close()
  }
}

/** TRTC SDK 已给出 48kHz/mono Float32 时，只负责饱和转换、20ms 切帧和编号。 */
export class Float32PCMEncoder {
  private pending: number[] = []
  private sequence = 0

  push(samples: Float32Array): RTCPCMFrame[] {
    for (const sample of samples) {
      const clamped = Math.max(-1, Math.min(1, sample))
      this.pending.push(clamped < 0 ? Math.round(clamped * 32768) : Math.round(clamped * 32767))
    }
    const frames: RTCPCMFrame[] = []
    while (this.pending.length >= 960) {
      const pcm = new Int16Array(this.pending.splice(0, 960))
      frames.push({ sequence: this.sequence, pcm })
      this.sequence = (this.sequence + 1) >>> 0
    }
    return frames
  }
}
