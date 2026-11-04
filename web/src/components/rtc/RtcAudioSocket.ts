import { createWsTicket } from '../../services/api/ws-ticket'
import { wsRtcAudioUrl } from '../../services/api/endpoint'
import type { RTCPCMFrame } from './RemoteAudioCapture'

const MAX_BUFFERED_BYTES = 256 * 1024

/** 公开采集页唯一 PCM 出口；操作员页面不构造此 socket。 */
export class RtcAudioSocket {
  private ws: WebSocket | null = null
  private generation = 0
  private rejectConnect: ((reason?: unknown) => void) | null = null

  async connect(sessionId: string, epoch: number): Promise<void> {
    const generation = ++this.generation
    const ticket = await createWsTicket(sessionId)
    if (generation !== this.generation) throw new DOMException('RTC 音频连接已取消', 'AbortError')
    const ws = new WebSocket(wsRtcAudioUrl(sessionId, epoch), ['dh.rtc-audio.v1', `ticket.${ticket.ticket}`])
    this.ws = ws
    await new Promise<void>((resolve, reject) => {
      this.rejectConnect = reject
      ws.onopen = () => {
        if (generation !== this.generation) return
        this.rejectConnect = null
        resolve()
      }
      ws.onerror = () => {
        if (generation !== this.generation) return
        this.rejectConnect = null
        reject(new Error('RTC 音频 WebSocket 连接失败'))
      }
      ws.onclose = () => {
        if (generation !== this.generation) return
        const pending = this.rejectConnect
        this.rejectConnect = null
        pending?.(new Error('RTC 音频 WebSocket 已关闭'))
        if (this.ws === ws) this.ws = null
      }
    })
  }

  send(frame: RTCPCMFrame): boolean {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN || ws.bufferedAmount > MAX_BUFFERED_BYTES) return false
    if (frame.pcm.length !== 960) throw new Error('RTC PCM 帧必须为 1920 字节')
    const buffer = new ArrayBuffer(4 + frame.pcm.byteLength)
    new DataView(buffer).setUint32(0, frame.sequence >>> 0, false)
    new Uint8Array(buffer, 4).set(new Uint8Array(frame.pcm.buffer, frame.pcm.byteOffset, frame.pcm.byteLength))
    ws.send(buffer)
    return true
  }

  close(): void {
    const pending = this.rejectConnect
    this.rejectConnect = null
    this.generation += 1
    const ws = this.ws
    this.ws = null
    if (ws) {
      ws.onopen = null
      ws.onerror = null
      ws.onclose = null
      ws.close()
    }
    pending?.(new DOMException('RTC 音频连接已取消', 'AbortError'))
  }
}
