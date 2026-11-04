import { afterEach, describe, expect, it, vi } from 'vitest'
import { VoiceSocket } from '../VoiceSocket'
import type { VoiceServerEvent } from '../types'

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []
  readyState = FakeWebSocket.CONNECTING
  bufferedAmount = 0
  sent: Array<string | ArrayBufferLike> = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null

  constructor(readonly url: string, readonly protocols: string[]) {
    FakeWebSocket.instances.push(this)
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }

  send(data: string | ArrayBufferLike): void {
    this.sent.push(data)
  }

  close(): void {
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
  }

  message(event: VoiceServerEvent): void {
    this.onmessage?.({ data: JSON.stringify(event) })
  }
}

function ticketResponse(): Response {
  return new Response(JSON.stringify({ ticket: 'voice-ticket', purpose: 'voice', expires_at: 1, ttl_s: 30 }), { status: 200 })
}

afterEach(() => {
  FakeWebSocket.instances = []
  vi.unstubAllGlobals()
})

describe('VoiceSocket', () => {
  it('使用独立语音 WS、一次性票据子协议并发送 begin 契约', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ticketResponse()))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new VoiceSocket({ onEvent: vi.fn() })
    const connected = socket.connect('s-1', 'client-a')
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toBe('ws://localhost:3000/ws/v1/voice?session_id=s-1&client_id=client-a')
    expect(ws.protocols).toEqual(['dh.voice.v1', 'ticket.voice-ticket'])
    ws.open()
    await connected
    socket.begin(7, 'ptt', 'turn-a')
    expect(JSON.parse(String(ws.sent[0]))).toEqual({
      type: 'begin', client_turn_id: 'turn-a', mode: 'ptt', format: 'pcm_s16le',
      sample_rate: 16000, channels: 1, frame_ms: 20, epoch: 7
    })
    socket.close()
  })

  it('发送 640 字节 PCM 并按实际发送帧数 end', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ticketResponse()))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new VoiceSocket({ onEvent: vi.fn() })
    const connected = socket.connect('s-1', 'client-a')
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    ws.open()
    await connected
    socket.begin(0, 'ptt', 'turn-a')
    socket.sendPCM(new Int16Array(320))
    expect(ws.sent[1]).toBeInstanceOf(ArrayBuffer)
    expect((ws.sent[1] as ArrayBuffer).byteLength).toBe(640)
    expect(() => socket.sendPCM(new Int16Array(10))).toThrow('640')
    socket.end()
    expect(JSON.parse(String(ws.sent[2]))).toEqual({ type: 'end', frames: 1 })
    socket.close()
  })

  it('透传回合事件和延迟时间点', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ticketResponse()))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const onEvent = vi.fn()
    const socket = new VoiceSocket({ onEvent })
    const connected = socket.connect('s-1', 'client-a')
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    ws.open()
    await connected
    const event: VoiceServerEvent = {
      type: 'voice.first_audio', turn_id: 'turn-a',
      timings: { speech_end_at_ms: 10, asr_final_at_ms: 20, llm_first_token_at_ms: 30, tts_first_audio_at_ms: 40 }
    }
    ws.message(event)
    expect(onEvent).toHaveBeenCalledWith(event)
    socket.close()
  })

  it('握手前关闭会拒绝 connect，不留下永久悬挂的 Promise', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ticketResponse()))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new VoiceSocket({ onEvent: vi.fn() })
    const connected = socket.connect('s-1', 'client-a')
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const rejection = expect(connected).rejects.toMatchObject({ name: 'AbortError' })
    socket.close()
    await rejection
  })

  it('cancel 发送真实取消控制帧', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ticketResponse()))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new VoiceSocket({ onEvent: vi.fn() })
    const connected = socket.connect('s-1', 'client-a')
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    ws.open()
    await connected
    socket.cancel()
    expect(JSON.parse(String(ws.sent[0]))).toEqual({ type: 'cancel' })
    socket.close()
  })
})
