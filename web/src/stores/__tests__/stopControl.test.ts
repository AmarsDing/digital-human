import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../services/api/live', () => ({
  getLiveStatus: vi.fn(),
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

vi.mock('../../services/api/platform', () => ({
  getStreamStatus: vi.fn(),
  getCollectorStatus: vi.fn(),
  saveStreamTarget: vi.fn(),
  saveCollectorConfig: vi.fn(),
  startStream: vi.fn(),
  stopStream: vi.fn()
}))

import { stopLive, getLiveStatus } from '../../services/api/live'
import { stopStream, getStreamStatus } from '../../services/api/platform'
import { useLiveStore } from '../live'
import { usePlatformStore } from '../platform'
import { useStopControlStore } from '../stopControl'

const stopLiveMock = vi.mocked(stopLive)
const stopStreamMock = vi.mocked(stopStream)
const liveStatusMock = vi.mocked(getLiveStatus)
const streamStatusMock = vi.mocked(getStreamStatus)

describe('stopControl 唯一下播/停推控制器（§3.2/§3.4）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    liveStatusMock.mockResolvedValue({ phase: 'live', sessionId: 's1', durationS: 10 } as never)
    streamStatusMock.mockResolvedValue({ state: 'pushing' } as never)
  })

  function seedLive() {
    const live = useLiveStore()
    live.status = { phase: 'live', sessionId: 's1', durationS: 10 } as never
    return live
  }

  function seedStream() {
    const platform = usePlatformStore()
    platform.stream = { state: 'pushing' } as never
    return platform
  }

  it('三个下播入口（stage/video-stage/composer/f10）均进入同一确认框；在途触发被忽略', () => {
    seedLive()
    const ctl = useStopControlStore()
    ctl.requestStopLive('video-stage')
    expect(ctl.liveConfirmOpen).toBe(true)
    expect(ctl.liveSource).toBe('video-stage')
    ctl.requestStopLive('composer')
    ctl.requestStopLive('f10')
    expect(ctl.liveSource).toBe('video-stage') // 在途忽略后续触发
    expect(ctl.liveConfirmOpen).toBe(true)
  })

  it('未直播中或 busy 时 requestStopLive 直接忽略', () => {
    const live = seedLive()
    const ctl = useStopControlStore()
    live.status = { phase: 'idle', sessionId: '', durationS: 0 } as never
    ctl.requestStopLive('f10')
    expect(ctl.liveConfirmOpen).toBe(false)
    seedLive()
    live.busy = true
    ctl.requestStopLive('f10')
    expect(ctl.liveConfirmOpen).toBe(false)
  })

  it('下播与停推各自确认、分别调用，状态不混报', async () => {
    seedLive()
    seedStream()
    stopLiveMock.mockResolvedValue({ phase: 'ended', ok: true } as never)
    stopStreamMock.mockResolvedValue({ ok: true } as never)
    const ctl = useStopControlStore()
    ctl.requestStopLive('stage')
    ctl.requestStopStream('stream-card')
    expect(ctl.liveConfirmOpen).toBe(true)
    expect(ctl.streamConfirmOpen).toBe(true)
    await ctl.confirmStopLive()
    expect(stopLiveMock).toHaveBeenCalledTimes(1)
    expect(stopStreamMock).not.toHaveBeenCalled() // 下播不伪报停推
    expect(ctl.liveConfirmOpen).toBe(false)
    expect(ctl.streamConfirmOpen).toBe(true) // 停推确认不受影响
    await ctl.confirmStopStream()
    expect(stopStreamMock).toHaveBeenCalledTimes(1)
    expect(stopLiveMock).toHaveBeenCalledTimes(1) // 停推不伪报下播
    expect(ctl.streamConfirmOpen).toBe(false)
  })

  it('双击/并发确认只产生一个 stop 请求', async () => {
    seedLive()
    let release: (v: unknown) => void = () => undefined
    stopLiveMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve
        }) as never
    )
    const ctl = useStopControlStore()
    ctl.requestStopLive('composer')
    const p1 = ctl.confirmStopLive()
    const p2 = ctl.confirmStopLive() // busy 期间再次确认无效
    expect(stopLiveMock).toHaveBeenCalledTimes(1)
    release({ phase: 'ended', ok: true })
    await p1
    await p2
    expect(stopLiveMock).toHaveBeenCalledTimes(1)
  })

  it('stop 失败：确认框保留、错误可感知、真实状态不变、可重试成功', async () => {
    const live = seedLive()
    stopLiveMock.mockRejectedValueOnce(new Error('boom'))
    const ctl = useStopControlStore()
    ctl.requestStopLive('f10')
    await ctl.confirmStopLive()
    expect(ctl.liveConfirmOpen).toBe(true) // 失败保留确认框可重试
    expect(ctl.liveError).toBeTruthy()
    expect(live.status?.phase).toBe('live') // 当前场真实状态保持
    stopLiveMock.mockResolvedValue({ phase: 'ended', ok: true } as never)
    await ctl.confirmStopLive()
    expect(stopLiveMock).toHaveBeenCalledTimes(2)
    expect(ctl.liveConfirmOpen).toBe(false)
    expect(ctl.liveError).toBe('')
  })

  it('Esc 取消：不调用 stop，状态不变', async () => {
    seedLive()
    const ctl = useStopControlStore()
    ctl.requestStopLive('stage')
    ctl.cancelStopLive()
    expect(ctl.liveConfirmOpen).toBe(false)
    expect(stopLiveMock).not.toHaveBeenCalled()
  })
})
