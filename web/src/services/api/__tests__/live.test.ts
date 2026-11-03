import { afterEach, describe, expect, it, vi } from 'vitest'
import { idemFor, normalizeRestEvent, normalizeRtcToken } from '../live'
import type { RestLiveEvent } from '../types'

describe('idemFor（幂等键 2s 时间片复用）', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('同 sessionId+action 在 2s 内复用同一键，跨片换键', () => {
    vi.useFakeTimers()
    vi.setSystemTime(100000)
    const a = idemFor('s1', 'live.start')
    const b = idemFor('s1', 'live.start')
    expect(a).toBe(b)
    vi.setSystemTime(100500)
    expect(idemFor('s1', 'live.start')).toBe(a)
    // 不同 action/场次不复用。
    expect(idemFor('s1', 'live.stop')).not.toBe(a)
    expect(idemFor('s2', 'live.start')).not.toBe(a)
    vi.setSystemTime(103000)
    expect(idemFor('s1', 'live.start')).not.toBe(a)
  })
})

describe('normalizeRestEvent（REST 轮询 → WS 事件信封）', () => {
  it('把 protojson 的字符串 int64 还原为数字并解出 payload', () => {
    const raw: RestLiveEvent = {
      seq: '7',
      eventId: 'evt-7',
      sessionId: 'sess-1',
      type: 'live.started',
      tsMs: '1793647962252',
      epoch: '1793647962252',
      requestId: 'rid-123',
      payloadJson: btoa('{"live_id":"fake-live"}')
    }
    const e = normalizeRestEvent(raw)
    expect(e.seq).toBe(7)
    expect(e.ts_ms).toBe(1793647962252)
    expect(e.epoch).toBe(1793647962252)
    expect(e.event_id).toBe('evt-7')
    expect(e.session_id).toBe('sess-1')
    expect(e.request_id).toBe('rid-123')
    expect(e.payload).toEqual({ live_id: 'fake-live' })
  })

  it('缺失字段有稳定兜底，坏 base64 不抛错', () => {
    const e = normalizeRestEvent({ seq: 1, type: 'live.stopped', payloadJson: '%%%' })
    expect(e.seq).toBe(1)
    expect(e.ts_ms).toBe(0)
    expect(e.request_id).toBe('')
    expect(e.payload).toBeUndefined()
  })
})

describe('normalizeRtcToken（RTC 凭据回复）', () => {
  it('有凭据：int64 字符串过期时间还原为数字', () => {
    const t = normalizeRtcToken({
      ok: true,
      mode: 'managed',
      provider: 'artc',
      token: 'rtc-dev.tk',
      expiresAt: '1800000600000',
      roomId: 'dh-s1',
      userId: 'viewer-s1'
    })
    expect(t).toMatchObject({
      ok: true,
      mode: 'managed',
      provider: 'artc',
      token: 'rtc-dev.tk',
      expiresAt: 1800000600000,
      roomId: 'dh-s1',
      userId: 'viewer-s1'
    })
  })

  it('无凭据：缺省字段兜底为 unconfigured/none，不产出 token', () => {
    const t = normalizeRtcToken({ ok: false, reason: 'rtc_not_configured' })
    expect(t.mode).toBe('unconfigured')
    expect(t.provider).toBe('none')
    expect(t.token).toBe('')
    expect(t.expiresAt).toBe(0)
    expect(t.reason).toBe('rtc_not_configured')
  })
})
