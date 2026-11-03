import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createWsTicket } from '../../services/api/ws-ticket'
import { useEventsStore } from '../events'

vi.mock('../../services/api/ws-ticket', () => ({ createWsTicket: vi.fn() }))

class FakeSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeSocket[] = []

  url: string
  protocols: string[]
  readyState = FakeSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(url: string, protocols: string[]) {
    this.url = url
    this.protocols = protocols
    FakeSocket.instances.push(this)
  }

  open(): void {
    this.readyState = FakeSocket.OPEN
    this.onopen?.()
  }

  // 真实 WebSocket 的 close 事件是异步派发的，这里用 0ms 定时器复刻，
  // 以便测试“旧连接关闭事件晚于新连接建立”的竞态。
  close(): void {
    if (this.readyState === FakeSocket.CLOSED) return
    this.readyState = FakeSocket.CLOSED
    setTimeout(() => this.onclose?.(), 0)
  }

  emit(data: unknown): void {
    this.onmessage?.({ data: JSON.stringify(data) })
  }
}

const ticketMock = vi.mocked(createWsTicket)

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0)
}

describe('events store WebSocket 连接代际', () => {
  beforeEach(async () => {
    vi.useFakeTimers()
    setActivePinia(createPinia())
    FakeSocket.instances = []
    vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
    ticketMock.mockReset()
    ticketMock.mockResolvedValue({ ticket: 't-1', expiresAt: 0, ttlS: 60 })
    // 清掉上一个用例遗留的模块级 socket/定时器。
    useEventsStore().disconnect()
    await flush()
  })

  afterEach(() => {
    useEventsStore().disconnect()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('快速重连后旧 socket 的 onclose 不会清空新连接，也不会再开第三条', async () => {
    const store = useEventsStore()
    const first = store.connect('s1')
    await flush()
    await first
    expect(FakeSocket.instances).toHaveLength(1)
    const a = FakeSocket.instances[0]
    a.open()
    expect(store.mode).toBe('ws')

    // 断开后立刻重连：a 的 close 事件要等到 flush 才派发。
    store.disconnect()
    const second = store.connect('s1')
    await flush()
    await second
    expect(FakeSocket.instances).toHaveLength(2)
    const b = FakeSocket.instances[1]
    b.open()
    expect(store.mode).toBe('ws')

    // 旧连接 a 的 onclose 此刻才到达（代际已过期，必须被忽略）。
    await flush()
    expect(store.mode).toBe('ws')
    expect(store.connected).toBe(true)

    // 再等两轮退避，不允许出现第三条连接。
    await vi.advanceTimersByTimeAsync(5000)
    expect(FakeSocket.instances).toHaveLength(2)
    expect(store.mode).toBe('ws')
  })

  it('只在换场次时清空事件与去重表，同场次不重置', async () => {
    const store = useEventsStore()
    store.switchSession('s1')
    store.accept({ seq: 1, event_id: 'a1', session_id: 's1', type: 'live.started', ts_ms: 1, epoch: 7, payload: {} })
    store.accept({ seq: 2, event_id: 'a2', session_id: 's1', type: 'song.playing', ts_ms: 2, epoch: 7, payload: {} })
    store.accept({ seq: 3, event_id: 'a3', session_id: 's1', type: 'refs.mounted', ts_ms: 3, epoch: 7, payload: {} })
    store.accept({ seq: 4, event_id: 'a4', session_id: 's1', type: 'action.sent', ts_ms: 4, epoch: 7, payload: {} })
    expect(store.events).toHaveLength(4)

    // 同一场次重复切换（状态轮询）不得清空。
    store.switchSession('s1')
    expect(store.events).toHaveLength(4)
    expect(store.seen.a1).toBe(true)

    // 换场次：清空事件、去重表与 seq 起点。
    store.switchSession('s2')
    expect(store.events).toHaveLength(0)
    expect(store.seen).toEqual({})
    expect(store.lastSeq).toBe(0)
    expect(store.epoch).toBe(0)

    // 新场次 seq 从 1 重新开始，旧场次 event_id 不参与去重。
    store.accept({ seq: 1, event_id: 'a1', session_id: 's2', type: 'live.started', ts_ms: 9, epoch: 8, payload: {} })
    expect(store.events).toHaveLength(1)
    expect(store.lastSeq).toBe(1)
  })

  it('连续 accept 3 条，drain 按序返回全部三条（批量回放不丢中间事件）', () => {
    const store = useEventsStore()
    store.switchSession('s1')
    store.accept({ seq: 1, event_id: 'b1', session_id: 's1', type: 'live.started', ts_ms: 1, epoch: 1, payload: {} })
    store.accept({ seq: 2, event_id: 'b2', session_id: 's1', type: 'transcript.final', ts_ms: 2, epoch: 1, payload: { text: '你好', index: 0 } })
    store.accept({ seq: 3, event_id: 'b3', session_id: 's1', type: 'live.stopped', ts_ms: 3, epoch: 1, payload: {} })
    expect(store.pending).toHaveLength(3)
    const drained = store.drain()
    expect(drained.map((e) => e.seq)).toEqual([1, 2, 3])
    expect(store.pending).toHaveLength(0)
    // drain 是消费语义：再取为空。
    expect(store.drain()).toEqual([])
  })

  it('取票失败进入轮询，退避后只重试一条连接且事件不重复', async () => {
    const store = useEventsStore()
    ticketMock.mockRejectedValueOnce(new Error('ticket down'))
    await store.connect('s2')
    expect(store.mode).toBe('poll')
    expect(FakeSocket.instances).toHaveLength(0)

    // 退避 1000ms 后自动重试。
    await vi.advanceTimersByTimeAsync(1000)
    expect(FakeSocket.instances).toHaveLength(1)
    const ws = FakeSocket.instances[0]
    ws.open()
    expect(store.mode).toBe('ws')

    // 同一事件重复投递只保留一条。
    const event = { seq: 1, event_id: 'e1', session_id: 's2', type: 'live.started', ts_ms: 1, epoch: 1, payload: {} }
    ws.emit(event)
    ws.emit(event)
    expect(store.events).toHaveLength(1)
    expect(store.lastSeq).toBe(1)

    // 稳定后不允许继续新建连接。
    await vi.advanceTimersByTimeAsync(5000)
    expect(FakeSocket.instances).toHaveLength(1)
    expect(store.mode).toBe('ws')
  })
})
