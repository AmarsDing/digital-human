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
import { ApiError } from '../../services/api/client'
import { useLiveStore } from '../live'

const statusMock = vi.mocked(getLiveStatus)
const preflightMock = vi.mocked(preflightLive)
const startMock = vi.mocked(startLive)

function readyPreflight() {
  return {
    checkId: 'check-1',
    preflightToken: 'pft-1',
    personaId: 'p1',
    revision: 7,
    ready: true,
    mode: 'public-live',
    draftId: 'draft-1',
    draftRevision: 3,
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

  it('按 preflight → 选择复核 → start 顺序透传同一绑定，不回传响应 mode', async () => {
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
      { personaId: 'p1', avatarId: 'a1', greeting: 'hello', draftId: 'draft-1', expectedRevision: 3 },
      guard
    )

    expect(ok).toBe(true)
    expect(calls).toEqual(['preflight', 'guard', 'start'])
    expect(preflightMock).toHaveBeenCalledWith({ personaId: 'p1', draftId: 'draft-1', expectedRevision: 3 })
    expect(startMock).toHaveBeenCalledWith({
      personaId: 'p1', avatarId: 'a1', greeting: 'hello', sessionId: '',
      draftId: 'draft-1', revision: 7, expectedRevision: 3,
      resultDigest: 'sha256:server-bound', preflightToken: 'pft-1'
    })
  })

  it.each([
    { field: 'personaId', patch: { personaId: '' } },
    { field: 'draftId', patch: { draftId: '' } },
    { field: 'draftRevision', patch: { draftRevision: 0 } },
    { field: 'revision', patch: { revision: undefined } },
    { field: 'resultDigest', patch: { resultDigest: '' } },
    { field: 'preflightToken', patch: { preflightToken: '' } }
  ])('preflight 缺 $field 时显式阻断，不用空值或 0 调 Start', async ({ field, patch }) => {
    preflightMock.mockResolvedValue({ ...readyPreflight(), ...patch } as never)
    const store = useLiveStore()
    const ok = await store.start({ personaId: 'p1', expectedRevision: 3 })

    expect(ok).toBe(false)
    expect(startMock).not.toHaveBeenCalled()
    expect(store.errorCode).toBe('preflight_contract_missing')
    expect(store.error).toContain(field)
  })

  it('public-live 缺 personaId 必须阻断 Start', async () => {
    preflightMock.mockResolvedValue({ ...readyPreflight(), personaId: '', mode: 'public-live' } as never)
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(startMock).not.toHaveBeenCalled()
    expect(store.errorCode).toBe('preflight_contract_missing')
    expect(store.error).toContain('personaId')
  })

  it('local-preview 缺 personaId 允许 Start，其余绑定字段原样透传', async () => {
    preflightMock.mockResolvedValue({ ...readyPreflight(), personaId: '', draftId: '', draftRevision: 0, mode: 'local-preview' } as never)
    startMock.mockResolvedValue({ sessionId: 's1', phase: 'live', liveId: 'l1', mode: 'local-preview' } as never)
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(true)
    expect(startMock).toHaveBeenCalledWith(expect.objectContaining({
      personaId: '',
      draftId: '',
      expectedRevision: 0,
      resultDigest: 'sha256:server-bound',
      preflightToken: 'pft-1'
    }))
    expect(startMock.mock.calls[0][0]).not.toHaveProperty('mode')
  })

  it('VITE_LIVE_MODE 不能自行声明预览态，校验只读 preflight 响应 mode', async () => {
    vi.stubEnv('VITE_LIVE_MODE', 'local-preview')
    preflightMock.mockResolvedValue({ ...readyPreflight(), personaId: '', mode: 'public-live' } as never)
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(preflightMock).toHaveBeenCalledWith({ personaId: '', draftId: '', expectedRevision: 0 })
    expect(preflightMock.mock.calls[0][0]).not.toHaveProperty('mode')
    expect(startMock).not.toHaveBeenCalled()
    expect(store.error).toContain('personaId')
  })

  it('mode_not_allowed 显示服务端模式拒绝原因且不换 mode 重试', async () => {
    preflightMock.mockRejectedValue(new ApiError(409, 'mode_not_allowed', '开播失败'))
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(store.errorCode).toBe('mode_not_allowed')
    expect(store.error).toBe('服务端未允许当前开播模式，请检查后端开播配置；前端不会切换模式重试')
    expect(store.error).not.toBe('开播失败')
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).not.toHaveBeenCalled()
  })

  it.each([
    {
      status: 409,
      code: 'draft_required',
      message: 'public-live 必须先选择并提交草稿，再携带草稿 ID 与版本重新预检；不能使用空草稿开播'
    },
    {
      status: 428,
      code: 'preflight_required',
      message: '开播预检凭证缺失、未知、过期或已消费，请重新预检后再开播'
    }
  ])('Preflight 返回 $status/$code 时专属阻断且不自动重试', async ({ status, code, message }) => {
    preflightMock.mockRejectedValue(new ApiError(status, code, '开播失败'))
    const store = useLiveStore()
    const ok = await store.start()

    expect(ok).toBe(false)
    expect(store.errorCode).toBe(code)
    expect(store.error).toBe(message)
    expect(store.error).not.toBe('开播失败')
    expect(preflightMock).toHaveBeenCalledTimes(1)
    expect(startMock).not.toHaveBeenCalled()
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
      code: 'preflight_contract_missing',
      messages: ['preflightToken']
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

  it.each([
    {
      status: 409,
      code: 'profile_changed',
      message: '预检后数字人档案或版本已变化，请前往档案页确认改动后重新预检'
    },
    {
      status: 428,
      code: 'preflight_expired',
      message: '开播预检凭证已过期，请重新预检后再开播'
    },
    {
      status: 428,
      code: 'preflight_required',
      message: '开播预检凭证缺失、未知、过期或已消费，请重新预检后再开播'
    }
  ])('Start 返回 $status/$code 时展示专属恢复文案', async ({ status, code, message }) => {
    startMock.mockRejectedValue(new ApiError(status, code, '开播失败'))
    const store = useLiveStore()
    const ok = await store.start({ personaId: 'p1', expectedRevision: 3 })

    expect(ok).toBe(false)
    expect(store.errorCode).toBe(code)
    expect(store.error).toBe(message)
    expect(store.error).not.toBe('开播失败')
    expect(startMock).toHaveBeenCalledTimes(1)
  })
})
