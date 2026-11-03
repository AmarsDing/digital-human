import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../../services/api/panels', async (importOriginal) => {
  const orig = await importOriginal<typeof import('../../../services/api/panels')>()
  return {
    ...orig,
    getDanmakuState: vi.fn(),
    listDanmakuPending: vi.fn(),
    setDanmakuMode: vi.fn(),
    confirmDanmaku: vi.fn(),
    dismissDanmaku: vi.fn()
  }
})

import { getDanmakuState, listDanmakuPending } from '../../../services/api/panels'
import DanmakuPanel from '../DanmakuPanel.vue'
import { useDanmakuRuntimeStore } from '../../../stores/danmakuRuntime'

const stateMock = vi.mocked(getDanmakuState)
const pendingMock = vi.mocked(listDanmakuPending)

describe('DanmakuPanel 只是选择器（v2 §2.2）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    stateMock.mockResolvedValue({ state: { mode: 'confirm', enabled: true, recent: [], top: [] } } as never)
    pendingMock.mockResolvedValue([
      { id: 'd1', userName: '甲', text: '主播好' },
      { id: 'd2', userName: '乙', text: '来了' }
    ] as never)
  })

  afterEach(() => {
    useDanmakuRuntimeStore().stop()
    vi.useRealTimers()
  })

  it('切 Tab（卸载面板）后轮询 timer 仍唯一运行，pendingCount 不丢', async () => {
    vi.useFakeTimers()
    const rt = useDanmakuRuntimeStore()
    rt.start() // Stage 持有
    const w = mount(DanmakuPanel)
    await vi.advanceTimersByTimeAsync(0)
    expect(rt.pendingCount).toBe(2)
    expect(w.text()).toContain('主播好')
    expect(w.text()).toContain('待确认 2')
    w.unmount() // 模拟切 Tab：组件卸载
    expect(rt.running).toBe(true) // 轮询不随组件卸载停止
    expect(rt.pendingCount).toBe(2) // 待确认数不丢
    // 轮询仍在刷新
    const calls = stateMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(stateMock.mock.calls.length).toBeGreaterThan(calls)
  })

  it('断网时面板显示持久错误与最后成功时间，待确认数不伪报 0', async () => {
    const rt = useDanmakuRuntimeStore()
    await rt.load()
    stateMock.mockRejectedValue(new Error('network down'))
    await rt.load()
    const w = mount(DanmakuPanel)
    expect(w.text()).toContain('待确认 2')
    expect(w.find('[role="alert"]').text()).toContain('network down')
    expect(w.text()).toContain('最后成功')
    w.unmount()
  })
})
