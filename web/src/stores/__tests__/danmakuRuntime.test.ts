import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../services/api/panels', async (importOriginal) => {
  const orig = await importOriginal<typeof import('../../services/api/panels')>()
  return {
    ...orig,
    getDanmakuState: vi.fn(),
    listDanmakuPending: vi.fn(),
    setDanmakuMode: vi.fn(),
    confirmDanmaku: vi.fn(),
    dismissDanmaku: vi.fn()
  }
})

import { confirmDanmaku, getDanmakuState, listDanmakuPending, setDanmakuMode } from '../../services/api/panels'
import { useDanmakuRuntimeStore } from '../danmakuRuntime'
import { useKouboDraftStore } from '../kouboDraft'
import { useChoreoDraftStore } from '../choreoDraft'
import { useRefsDraftStore } from '../refsDraft'

const stateMock = vi.mocked(getDanmakuState)
const pendingMock = vi.mocked(listDanmakuPending)
const modeMock = vi.mocked(setDanmakuMode)
const confirmMock = vi.mocked(confirmDanmaku)

function dmState(mode = 'confirm', enabled = true) {
  return { state: { mode, enabled, recent: ['你好'], top: [{ text: '在吗', count: 2 }] } } as never
}

describe('danmakuRuntime（v2 §2.2/§2.4 三层合同）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    stateMock.mockResolvedValue(dmState())
    pendingMock.mockResolvedValue([{ id: 'd1', userName: 'u', text: 't1' }] as never)
  })

  afterEach(() => {
    useDanmakuRuntimeStore().stop()
    vi.useRealTimers()
  })

  it('start 幂等保持唯一 timer；重复 start 不新建调度', async () => {
    vi.useFakeTimers()
    const spy = vi.spyOn(globalThis, 'setInterval')
    const rt = useDanmakuRuntimeStore()
    rt.start()
    rt.start()
    rt.start()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(rt.running).toBe(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(stateMock).toHaveBeenCalled()
    rt.stop()
    expect(rt.running).toBe(false)
  })

  it('document.hidden 暂停轮询；恢复可见先立即刷新再续同一调度', async () => {
    vi.useFakeTimers()
    const rt = useDanmakuRuntimeStore()
    rt.start()
    await vi.advanceTimersByTimeAsync(0)
    const base = stateMock.mock.calls.length
    // 隐藏：interval tick 不再触发 load
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
    expect(stateMock.mock.calls.length).toBe(base)
    // 恢复可见：立即补一次刷新
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(stateMock.mock.calls.length).toBe(base + 1)
    rt.stop()
  })

  it('断网持久错误：pendingCount 不伪报 0，lastSuccessAt 不被伪造', async () => {
    const rt = useDanmakuRuntimeStore()
    await rt.load()
    expect(rt.pendingCount).toBe(1)
    const successAt = rt.lastSuccessAt
    expect(successAt).toBeGreaterThan(0)
    stateMock.mockRejectedValue(new Error('network down'))
    await rt.load()
    expect(rt.requestError).toBeTruthy()
    expect(rt.pendingCount).toBe(1) // 不伪报 0
    expect(rt.lastSuccessAt).toBe(successAt) // 不伪造成功时间
  })

  it('确认/忽略失败重读服务端为准，不假装成功', async () => {
    const rt = useDanmakuRuntimeStore()
    await rt.load()
    confirmMock.mockRejectedValue(new Error('boom'))
    await rt.decide('d1', true)
    expect(rt.requestError).toBeTruthy()
    // 失败后以服务端为准重读
    expect(stateMock.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('模式提交期间标记「提交中/未应用」，不改 Badge 假装成功', async () => {
    const rt = useDanmakuRuntimeStore()
    await rt.load()
    expect(rt.mode).toBe('confirm')
    let release: (v: unknown) => void = () => undefined
    modeMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve
        }) as never
    )
    const p = rt.applyMode('auto')
    expect(rt.modeApplying).toBe(true)
    expect(rt.mode).toBe('confirm') // 未被接受前仍是服务端有效值
    stateMock.mockResolvedValue(dmState('auto')) // 服务端接受后返回新模式
    release({ ok: true, changed: true, mode: 'auto' })
    await p
    expect(rt.modeApplying).toBe(false)
    expect(rt.mode).toBe('auto')
  })
})

describe('跨 Tab 草稿（v2 §2.2/§2.5）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('kouboDraft：dirty 草稿不被服务端刷新覆盖，往返保留', () => {
    const d = useKouboDraftStore()
    d.syncFromServer('服务端文本')
    expect(d.text).toBe('服务端文本')
    d.setText('用户未提交草稿')
    d.setMaxChars(200)
    d.togglePick('seg-1')
    // 模拟切 Tab 卸载组件后的服务端刷新
    d.syncFromServer('服务端新文本')
    expect(d.text).toBe('用户未提交草稿')
    expect(d.maxChars).toBe(200)
    expect(d.picked).toEqual(['seg-1'])
    // 提交成功后清 dirty，服务端文本可再次同步
    d.markSubmitted()
    d.syncFromServer('服务端新文本')
    expect(d.text).toBe('服务端新文本')
  })

  it('choreoDraft：编排编辑表单往返保留', () => {
    const d = useChoreoDraftStore()
    d.setLibName('新库A')
    d.setActionName('dance3')
    expect(d.dirty).toBe(true)
    // 组件卸载重建后（同一 pinia）草稿仍在
    const again = useChoreoDraftStore()
    expect(again.libName).toBe('新库A')
    expect(again.actionName).toBe('dance3')
  })

  it('refsDraft：待下发草稿不被服务端刷新覆盖；快照最多 3 组', () => {
    const d = useRefsDraftStore()
    d.syncFromServer([{ image_id: 's', url: 'u0' }])
    expect(d.pending).toHaveLength(1)
    d.add({ image_id: 'a', url: 'u1' })
    d.syncFromServer([{ image_id: 's', url: 'u0' }])
    expect(d.pending).toHaveLength(2) // dirty 草稿保留
    for (let i = 0; i < 5; i++) {
      d.pushSnapshot({ images: [], http: 'ok', httpError: '', ack: 'success', ackError: '', atMs: i })
    }
    expect(d.snapshots).toHaveLength(3)
    expect(d.snapshots[0].atMs).toBe(4) // 最新在前
    expect(d.canUndo).toBe(true)
  })
})
