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

import { getLiveStatus, startLive } from '../../services/api/live'
import { useLiveStore } from '../live'

const statusMock = vi.mocked(getLiveStatus)
const startMock = vi.mocked(startLive)

describe('live store 开播互斥', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    statusMock.mockResolvedValue({ phase: 'idle', sessionId: '', durationS: 0 } as never)
  })

  it('连点两次 start 只发一次后端请求', async () => {
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
    expect(startMock).toHaveBeenCalledTimes(1)
    release({ sessionId: 's1', phase: 'starting', liveId: 'l1' })
    await p1
    await p2
    expect(startMock).toHaveBeenCalledTimes(1)
    expect(store.busy).toBe(false)
  })
})
