import { afterEach, describe, expect, it, vi } from 'vitest'
import { RtcAudioSocket } from '../RtcAudioSocket'

class FakeWebSocket {
  static readonly OPEN = 1
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []
  readyState = 0
  bufferedAmount = 0
  sent: ArrayBuffer[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string, readonly protocols: string[]) { FakeWebSocket.instances.push(this) }
  open(): void { this.readyState = 1; this.onopen?.() }
  send(data: ArrayBuffer): void { this.sent.push(data) }
  close(): void { this.readyState = 3; this.onclose?.() }
}

afterEach(() => {
  FakeWebSocket.instances = []
  vi.unstubAllGlobals()
})

describe('RtcAudioSocket', () => {
  it('复用一次性票据，只发送 sequence + 1920 字节 PCM', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      ticket: 'rtc-audio-ticket', expires_at: 1, ttl_s: 30
    }), { status: 200 })))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new RtcAudioSocket()
    const connecting = socket.connect('s-1', 7)
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toBe('ws://localhost:3000/ws/v1/rtc-audio?session_id=s-1&epoch=7')
    expect(ws.protocols).toEqual(['dh.rtc-audio.v1', 'ticket.rtc-audio-ticket'])
    ws.open()
    await connecting

    socket.send({ sequence: 258, pcm: new Int16Array(960) })
    expect(ws.sent).toHaveLength(1)
    expect(ws.sent[0].byteLength).toBe(1924)
    expect(new DataView(ws.sent[0]).getUint32(0, false)).toBe(258)
    socket.close()
  })

  it('断线后停发，不缓存到下一次连接', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ ticket: 't' }), { status: 200 })))
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const socket = new RtcAudioSocket()
    const connecting = socket.connect('s-1', 1)
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    ws.open()
    await connecting
    ws.close()
    expect(socket.send({ sequence: 1, pcm: new Int16Array(960) })).toBe(false)
    expect(ws.sent).toHaveLength(0)
  })
})
