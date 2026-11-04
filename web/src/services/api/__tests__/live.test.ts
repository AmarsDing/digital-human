import { afterEach, describe, expect, it, vi } from 'vitest'
import { idemFor, normalizeRestEvent, normalizeRtcToken, startLive } from '../live'
import * as liveApi from '../live'
import type { RestLiveEvent } from '../types'

interface PreflightResultForTest {
  ready: boolean
  preflightToken: string
  personaId?: string
  revision?: number
  expiresAtMs: number
  resultDigest: string
  checks: Array<{ code: string; status: string; detail?: string }>
}

type PreflightForTest = (input: {
  personaId?: string
  draftId?: string
  expectedRevision?: number
}) => Promise<PreflightResultForTest>

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, text: async () => JSON.stringify(body) } as Response
}

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

describe('live preflight/start HTTP 请求体', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('preflight 使用后端 snake_case 契约并归一化 token/checks/digest', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      check_id: 'check-1', preflight_token: 'pft-1', persona_id: 'p1', revision: '7', ready: true, mode: 'public-live',
      draft_id: '', draft_revision: 0, input_revisions: { avatar: 7 },
      result_digest: 'sha256:bound', observed_at_ms: 1000, expires_at_ms: 31000,
      checks: [{ code: 'vidu', status: 'pass', blocking: true, source: 'probe', observed_at_ms: 1000 }]
    }))
    vi.stubGlobal('fetch', fetchMock)
    const preflight = (liveApi as { preflightLive?: PreflightForTest }).preflightLive
    expect(preflight).toBeTypeOf('function')

    const result = await preflight!({ personaId: 'p1', draftId: 'draft-1', expectedRevision: 0 })
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/v1/live/preflight')
    expect(init.method).toBe('POST')
    const body = JSON.parse(String(init.body)) as Record<string, unknown>
    expect(body).toEqual({ persona_id: 'p1', draft_id: 'draft-1', expected_revision: 0 })
    expect(body).not.toHaveProperty('mode')
    expect(result).toMatchObject({
      ready: true, preflightToken: 'pft-1', personaId: 'p1', revision: 7,
      expiresAtMs: 31000, resultDigest: 'sha256:bound'
    })
    expect(result.checks[0]).toMatchObject({ code: 'vidu', status: 'pass' })
  })

  it('Start 透传 persona/draft/revision/digest/token，但不回传 mode', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      sessionId: 's1', phase: 'live', liveId: 'l1', mode: 'public-live'
    }))
    vi.stubGlobal('fetch', fetchMock)

    const reply = await startLive({
      sessionId: '', personaId: 'p1', avatarId: 'a1', greeting: 'hello',
      draftId: 'draft-1', revision: 7, expectedRevision: 7,
      resultDigest: 'sha256:bound', preflightToken: 'pft-1'
    })
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const body = JSON.parse(String(init.body)) as Record<string, unknown>
    expect(path).toBe('/api/v1/live/start')
    expect(init.method).toBe('POST')
    expect(body).toMatchObject({
      personaId: 'p1', avatarId: 'a1', greeting: 'hello',
      draftId: 'draft-1', revision: 7, expectedRevision: 7, resultDigest: 'sha256:bound', preflightToken: 'pft-1'
    })
    expect(body).not.toHaveProperty('mode')
    expect(reply.mode).toBe('public-live')
  })
})
