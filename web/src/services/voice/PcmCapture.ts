import type { VoiceCapture } from './types'

export function voiceBrowserIssue(): string {
  if (!window.isSecureContext) return '麦克风采集需要 HTTPS 或 localhost；仍可使用文字输入'
  if (!navigator.mediaDevices?.getUserMedia) return '当前浏览器无可用麦克风接口；仍可使用文字输入'
  if (typeof AudioContext === 'undefined' || typeof AudioWorkletNode === 'undefined') {
    return '当前浏览器不支持 AudioWorklet；仍可使用文字输入'
  }
  return ''
}

export class PcmCapture implements VoiceCapture {
  private stream: MediaStream | null = null
  private context: AudioContext | null = null
  private source: MediaStreamAudioSourceNode | null = null
  private node: AudioWorkletNode | null = null
  private sink: GainNode | null = null
  private generation = 0

  async start(onFrame: (pcm: Int16Array) => void): Promise<void> {
    if (this.stream) return
    const issue = voiceBrowserIssue()
    if (issue) throw new Error(issue)
    const generation = ++this.generation
    try {
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true }
      })
      if (generation !== this.generation) {
        stream.getTracks().forEach((track) => track.stop())
        throw new DOMException('麦克风采集已取消', 'AbortError')
      }
      this.stream = stream
      this.context = new AudioContext()
      await this.context.resume()
      await this.context.audioWorklet.addModule('/audio/pcm16-worklet.js')
      this.source = this.context.createMediaStreamSource(this.stream)
      this.node = new AudioWorkletNode(this.context, 'pcm16-worklet')
      this.sink = this.context.createGain()
      this.sink.gain.value = 0
      this.node.port.onmessage = (event: MessageEvent<Int16Array>) => {
        if (event.data instanceof Int16Array && event.data.length === 320) onFrame(event.data)
      }
      this.source.connect(this.node)
      this.node.connect(this.sink)
      this.sink.connect(this.context.destination)
    } catch (error) {
      await this.stop()
      throw error
    }
  }

  async stop(): Promise<void> {
    this.generation += 1
    this.node && (this.node.port.onmessage = null)
    this.source?.disconnect()
    this.node?.disconnect()
    this.sink?.disconnect()
    this.stream?.getTracks().forEach((track) => track.stop())
    const context = this.context
    this.stream = null
    this.context = null
    this.source = null
    this.node = null
    this.sink = null
    if (context && context.state !== 'closed') await context.close()
  }
}
