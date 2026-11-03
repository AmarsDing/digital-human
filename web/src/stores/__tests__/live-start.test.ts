import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../services/api/live', () => ({
  getLiveStatus: vi.fn(),
  preflightLive: vi.fn(),
  startLive: vi.fn(),
  stopLive: vi.fn(),
  say: vi.fn(),
  sendCommand: vi.fn(),
  interrupt: vi.fn(),
  clearTranscript: vi.fn(),
  stopActions: vi.fn(),
  clearActions: vi.fn(),
  resetLlm: vi.fn(),
  listEvents: vi.fn()
}))

import { getLiveStatus, preflightLive, startLive } from '../../services/api/live'
import { useLiveStore } from '../live'

const statusMock = vi.mocked(getLiveStatus)
const preflightMock = vi.mocked(preflightLive)
const startMock = vi.mocked(startLive)

function readyPreflight() {
  return {
    checkId: 'check-1',
    preflightToken: 'pft-1',
    ready: true,
    mode: 'public-live',
    draftId: '',
    draftRevision: 0,
    inputRevisions: { avatar: 7 },
    resultDigest: 'sha256:server-bound',
    observedAtMs: Date.now(),
    expiresAtMs: Date.now() + 30_000,
    checks: [{ code: 'vidu', status: 'pass', blocking: true, source: 'test', observedAtMs: Date.now() }]
  }
}

describe('live store 严格 preflight 开播', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.resetAllMocks()
    statusMock.mockResolvedValue({ phase: 'idle', sessionId: '', durationS: 0 } as never)
    preflightMock.mockResolvedValue(readyPreflight() as never)
    startMock.mockResolvedValue({ sessionId: 's1', phase: 'live', liveId: 'l1', mode: 'public-live' } as never)
  })

  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it('连点两次 start 只发一次预检和一次后端请求', async () => {
    let release: (v: unknown) => void = () => undefined
    startMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve
        }) as never
    )
    const store = useLiveStore()
    const p1 = store.start()
    const p2 = store.start() // 在途互斥：直接复用同一 flight
    try {
      expect(preflightMock).toHaveBeenCalledTimes(1)
      await vi.waitFor(() => expect(startMock).toHaveBeenCalledTimes(1))
    } finally {
      release({ sessionId: 's1', phase: 'starting', liveId: 'l1', mode: 'public-live' })
      await p1
      await p2
    }
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).toHaveBeenCalledTimes(1)
    expect(store.busy).toBe(false)
  })

  it('默认 public-live，按 preflight → 选择复核 → start 顺序透传同一绑定', async () => {
    const calls: string[] = []
    preflightMock.mockImplementation(async () => {
      calls.push('preflight')
      return readyPreflight() as never
    })
    const guard = vi.fn(async () => {
      calls.push('guard')
      return true
    })
    startMock.mockImplementation(async () => {
      calls.push('start')
      return { sessionId: 's1', phase: 'live', liveId: 'l1', mode: 'public-live' } as never
    })
    const store = useLiveStore()
    const ok = await store.start(
      { personaId: 'p1', avatarId: 'a1', greeting: 'hello', draftId: '', expectedRevision: 0 },
      guard
    )

    expect(ok).toBe(true)
    expect(calls).toEqual(['preflight', 'guard', 'start'])
    expect(preflightMock).toHaveBeenCalledWith({
      mode: 'public-live', personaId: 'p1', draftId: '', expectedRevision: 0
    })
    expect(startMock).toHaveBeenCalledWith({
      personaId: 'p1', avatarId: 'a1', greeting: 'hello', sessionId: '',
      mode: 'public-live', draftId: '', expectedRevision: 0, preflightToken: 'pft-1'
    })
    // digest 由服务端 token 存证绑定；Start 契约没有该字段，禁止伪造透传。
    expect(startMock.mock.calls[0][0]).not.toHaveProperty('resultDigest')
  })

  it('显式 local-preview 原样用于预检和 Start，不从 public 失败自动降级', async () => {
    preflightMock.mockResolvedValue({ ...readyPreflight(), mode: 'local-preview' } as never)
    startMock.mockResolvedValue({ sessionId: 's1', phase: 'live', liveId: 'l1', mode: 'local-preview' } as never)
    const store = useLiveStore()
    await store.start({ mode: 'local-preview' })

    expect(preflightMock).toHaveBeenCalledWith(expect.objectContaining({ mode: 'local-preview' }))
    expect(startMock).toHaveBeenCalledWith(expect.objectContaining({ mode: 'local-preview' }))
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).toHaveBeenCalledTimes(1)
  })

  it('生产构建仅在显式 VITE_LIVE_MODE=local-preview 时使用本地预览', async () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('VITE_LIVE_MODE', 'local-preview')
    preflightMock.mockResolvedValue({ ...readyPreflight(), mode: 'local-preview' } as never)
    const store = useLiveStore()
    await store.start()

    expect(preflightMock).toHaveBeenCalledWith(expect.objectContaining({ mode: 'local-preview' }))
    expect(startMock).toHaveBeenCalledWith(expect.objectContaining({ mode: 'local-preview' }))
  })

  it.each([
    {
      name: 'non-ready',
      patch: {
        ready: false,
        checks: [
          { code: 'vidu', status: 'fail', blocking: true, source: 'test', detail: 'missing credential', observedAtMs: 1 },
          { code: 'push_gate', status: 'unknown', blocking: true, source: 'test', detail: 'still probing', observedAtMs: 1 }
        ]
      },
      code: 'preflight_not_ready',
      messages: ['vidu=fail', 'missing credential', 'push_gate=unknown', 'still probing']
    },
    {
      name: 'unknown check',
      patch: { checks: [{ code: 'tts', status: 'unknown', blocking: true, source: 'test', observedAtMs: 1 }] },
      code: 'preflight_not_ready',
      messages: ['tts=unknown']
    },
    {
      name: 'running check',
      patch: { checks: [{ code: 'push_gate', status: 'running', blocking: true, source: 'test', observedAtMs: 1 }] },
      code: 'preflight_not_ready',
      messages: ['push_gate=running']
    },
    {
      name: 'expired token',
      patch: { expiresAtMs: Date.now() - 1 },
      code: 'preflight_required',
      messages: ['过期']
    },
    {
      name: 'missing token',
      patch: { preflightToken: '' },
      code: 'preflight_required',
      messages: ['凭证']
    }
  ])('$name 时只有 preflight，不创建半开播状态', async ({ patch, code, messages }) => {
    preflightMock.mockResolvedValue({ ...readyPreflight(), ...patch } as never)
    const store = useLiveStore()
    const ok = await store.start({ personaId: 'p1' })

    expect(ok).toBe(false)
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).not.toHaveBeenCalled()
    expect(store.phase).toBe('idle')
    expect(store.errorCode).toBe(code)
    for (const message of messages) expect(store.error).toContain(message)
  })

  it('preflight 网络错误不调用 Start，并沿用可访问错误文案', async () => {
    preflightMock.mockRejectedValue(new TypeError('fetch failed'))
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(startMock).not.toHaveBeenCalled()
    expect(store.error).toBe('无法连接后端服务')
    expect(store.phase).toBe('idle')
  })

  it('预检后 persona/revision 复核失败则要求重检且不 Start', async () => {
    const store = useLiveStore()
    const ok = await store.start({ personaId: 'p1' }, async () => false)

    expect(ok).toBe(false)
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).not.toHaveBeenCalled()
    expect(store.errorCode).toBe('preflight_required')
    expect(store.error).toContain('档案或版本已变化')
  })

  it('Start 自身失败不自动重试', async () => {
    startMock.mockRejectedValue(new Error('start failed'))
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).toHaveBeenCalledTimes(1)
  })
})
