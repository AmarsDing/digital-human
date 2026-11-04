import { afterEach, describe, expect, it, vi } from 'vitest'
import { createVoiceTicket, getVoiceStatus } from '../../api/voice'
import { wsVoiceUrl } from '../../api/endpoint'

afterEach(() => vi.unstubAllGlobals())

describe('voice API contract', () => {
  it('读取真实能力矩阵，不把 HTTP 成功改写为 ready', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      ready: false,
      asr: { source: 'unconfigured', ready: false, error_code: 'asr_unavailable' },
      llm: { source: 'unknown', ready: true },
      tts: { source: 'fake', ready: true },
      live: { source: 'unknown', ready: true },
      default_mode: 'ptt', continuous: true, audio_retained: false,
      interrupt_songs: false, max_audio_ms: 20000
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const status = await getVoiceStatus()
    expect(status.ready).toBe(false)
    expect(status.asr.error_code).toBe('asr_unavailable')
    expect(status.tts.source).toBe('fake')
  })

  it('语音票据绑定 session 与 client，不复用事件 WS 票据', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      ticket: 'voice-ticket', purpose: 'voice', expires_at: 123, ttl_s: 30
    }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const ticket = await createVoiceTicket('s 1', 'browser/1')
    expect(ticket.ticket).toBe('voice-ticket')
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/voice/ticket?session_id=s%201&client_id=browser%2F1')
    expect(fetchMock.mock.calls[0][1].method).toBe('POST')
  })

  it('语音 WS 使用独立端点、开发端口和绑定查询参数', () => {
    vi.stubGlobal('window', {
      location: { protocol: 'http:', host: 'localhost:9000', hostname: 'localhost' }
    })
    expect(wsVoiceUrl('s-1', 'client-a')).toBe('ws://localhost:9000/ws/v1/voice?session_id=s-1&client_id=client-a')
  })
})
