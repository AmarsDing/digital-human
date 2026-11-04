import { createVoiceTicket } from '../api/voice'
import { wsVoiceUrl } from '../api/endpoint'
import type { VoiceMode, VoiceServerEvent, VoiceTransport, VoiceTransportOptions } from './types'

const MAX_BUFFERED_BYTES = 256 * 1024

export class VoiceSocket implements VoiceTransport {
  private ws: WebSocket | null = null
  private generation = 0
  private frames = 0
  private intentionalClose = false
  private rejectConnect: ((reason?: unknown) => void) | null = null

  constructor(private readonly options: VoiceTransportOptions) {}

  async connect(sessionId: string, clientId: string): Promise<void> {
    const generation = ++this.generation
    this.intentionalClose = false
    const ticket = await createVoiceTicket(sessionId, clientId)
    if (generation !== this.generation) throw new DOMException('语音连接已取消', 'AbortError')
    const ws = new WebSocket(wsVoiceUrl(sessionId, clientId), ['dh.voice.v1', `ticket.${ticket.ticket}`])
    this.ws = ws
    await new Promise<void>((resolve, reject) => {
      this.rejectConnect = reject
      ws.onopen = () => {
        if (generation !== this.generation) return
        this.rejectConnect = null
        resolve()
      }
      ws.onmessage = (message) => {
        if (generation !== this.generation) return
        try {
          this.options.onEvent(JSON.parse(String(message.data)) as VoiceServerEvent)
        } catch {
          this.options.onEvent({ type: 'rejected', error: 'invalid_server_event', message: '语音事件解析失败' })
        }
      }
      ws.onerror = () => {
        if (generation !== this.generation) return
        this.rejectConnect = null
        reject(new Error('语音 WebSocket 连接失败'))
      }
      ws.onclose = () => {
        if (generation !== this.generation) return
        const rejectPending = this.rejectConnect
        this.rejectConnect = null
        rejectPending?.(new Error('语音 WebSocket 连接已关闭'))
        if (this.ws === ws) this.ws = null
        this.options.onClose?.(this.intentionalClose)
      }
    })
  }

  begin(epoch: number, mode: VoiceMode, clientTurnId: string): void {
    this.frames = 0
    this.send({
      type: 'begin', client_turn_id: clientTurnId, mode, format: 'pcm_s16le',
      sample_rate: 16000, channels: 1, frame_ms: 20, epoch
    })
  }

  sendPCM(pcm: Int16Array): void {
    if (pcm.length !== 320) throw new Error('PCM 帧必须为 640 字节')
    const ws = this.requireOpen()
    if (ws.bufferedAmount > MAX_BUFFERED_BYTES) throw new Error('语音连接拥塞，请稍后重试')
    const buffer = pcm.buffer.slice(pcm.byteOffset, pcm.byteOffset + pcm.byteLength) as ArrayBuffer
    ws.send(buffer)
    this.frames += 1
  }

  end(): void {
    this.send({ type: 'end', frames: this.frames })
  }

  cancel(): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.send({ type: 'cancel' })
  }

  close(): void {
    const rejectConnect = this.rejectConnect
    this.rejectConnect = null
    this.generation += 1
    this.intentionalClose = true
    const ws = this.ws
    this.ws = null
    if (ws) {
      ws.onopen = null
      ws.onmessage = null
      ws.onerror = null
      ws.onclose = null
      ws.close()
    }
    rejectConnect?.(new DOMException('语音连接已取消', 'AbortError'))
  }

  private requireOpen(): WebSocket {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) throw new Error('语音连接未就绪')
    return this.ws
  }

  private send(value: Record<string, unknown>): void {
    this.requireOpen().send(JSON.stringify(value))
  }
}
