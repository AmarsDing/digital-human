import { describe, expect, it } from 'vitest'
import type { RtcToken } from '../../../services/api/types'
import type { RtcCreds } from '../types'
import { REFRESH_LEAD_MS, refreshDelay, degradeOnRefreshError, toCreds } from '../creds'

const NOW = 1_800_000_000_000

function token(overrides: Partial<RtcToken> = {}): RtcToken {
  return {
    ok: true,
    mode: 'managed',
    provider: 'artc',
    token: 'rtc-dev.abc',
    expiresAt: NOW + 10 * 60 * 1000,
    roomId: 'dh-s1',
    userId: 'viewer-s1',
    reason: '',
    ...overrides
  }
}

describe('RTC 凭据', () => {
  it('无凭据：unconfigured/ok=false 时不产出 creds（不伪造入会）', () => {
    expect(toCreds(token({ ok: false, mode: 'unconfigured', token: '', provider: 'none', expiresAt: 0 }))).toBeNull()
    expect(toCreds(token({ ok: true, token: '' }))).toBeNull()
    expect(toCreds(token({ ok: true, roomId: '' }))).toBeNull()
  })

  it('有凭据：透传 token/过期时间/room/user id', () => {
    const creds = toCreds(token())
    expect(creds).toMatchObject({
      mode: 'managed',
      provider: 'artc',
      token: 'rtc-dev.abc',
      roomId: 'dh-s1',
      userId: 'viewer-s1',
      tokenExpireAt: NOW + 10 * 60 * 1000
    })
    expect(toCreds(token({ provider: 'trtc' }))?.provider).toBe('trtc')
  })

  it('过期前刷新：刷新时刻早于到期，且留足余量', () => {
    const expiresAt = NOW + 10 * 60 * 1000
    const delay = refreshDelay(expiresAt, NOW)
    expect(delay).toBeGreaterThan(0)
    expect(delay).toBe(10 * 60 * 1000 - REFRESH_LEAD_MS)
    expect(NOW + delay).toBeLessThan(expiresAt)

    // 短 TTL（小于两倍余量）取剩余一半，避免 1s 抖动重取。
    const short = refreshDelay(NOW + 20_000, NOW)
    expect(short).toBe(10_000)
    // 已过期立即刷新。
    expect(refreshDelay(NOW - 1, NOW)).toBe(0)
    // 无过期时间不排刷新。
    expect(refreshDelay(0, NOW)).toBe(-1)
  })

  it('刷新失败降级：保留旧凭据继续用到到期，只透出错误', () => {
    const prev: RtcCreds = toCreds(token()) as RtcCreds
    const out = degradeOnRefreshError(prev, '刷新失败：网络不可用')
    expect(out.creds).toBe(prev)
    expect(out.error).toContain('网络不可用')

    const none = degradeOnRefreshError(null, 'ticket_invalid_or_used')
    expect(none.creds).toBeNull()
    expect(none.error).toBe('ticket_invalid_or_used')
  })
})
