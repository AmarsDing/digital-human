import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../../services/api/panels', async (importOriginal) => {
  const orig = await importOriginal<typeof import('../../../services/api/panels')>()
  return {
    ...orig,
    getRefs: vi.fn(),
    listRefFiles: vi.fn(),
    switchRefs: vi.fn(),
    removeRefs: vi.fn(),
    uploadRefFile: vi.fn()
  }
})

import { getRefs, listRefFiles, normalizeRefsState, switchRefs } from '../../../services/api/panels'
import { ApiError } from '../../../services/api/client'
import RefsPanel from '../RefsPanel.vue'
import { useRefsDraftStore } from '../../../stores/refsDraft'

const getRefsMock = vi.mocked(getRefs)
const listFilesMock = vi.mocked(listRefFiles)
const switchMock = vi.mocked(switchRefs)

function stateFixture(over: Record<string, unknown> = {}) {
  return { images: [], source: 'fake', ...over } as never
}

describe('RefsPanel §5.1/§5.2', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    getRefsMock.mockResolvedValue(stateFixture())
    listFilesMock.mockResolvedValue([])
  })

  it('API 边界归一：images:null fixture 归一为空数组（§5.1）', () => {
    const st = normalizeRefsState({ images: null, source: 'fake' } as never)
    expect(st.images).toEqual([])
  })

  it('images:null fixture：API 边界归一数组，渲染不崩，已下发显示 0', async () => {
    // 模拟真实 API 模块行为：边界处已归一化。
    getRefsMock.mockImplementation(async () => normalizeRefsState({ images: null, source: 'fake' } as never))
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('已下发 0 张')
    expect(w.find('[role="alert"]').exists()).toBe(false)
    w.unmount()
  })

  it('文件列表 404：已下发状态与切图仍可用，文件区独立报错可重试', async () => {
    getRefsMock.mockResolvedValue(stateFixture({ images: [{ image_id: 'a', url: 'u1' }] }))
    listFilesMock.mockRejectedValue(new ApiError(404, 'not_found', 'no dir'))
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('已下发 1 张')
    expect(w.text()).toContain('文件列表加载失败')
    // 切图仍可用：添加并下发
    const draft = useRefsDraftStore()
    draft.add({ image_id: 'b', url: 'u2' })
    switchMock.mockResolvedValue(stateFixture({ images: [{ image_id: 'b', url: 'u2' }], ack: { success: true, at_ms: 1 } }))
    await w.findAll('button').find((b) => b.text() === '下发切换')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(switchMock).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('已下发并已应用')
    w.unmount()
  })

  it('文件列表 500 同样局部失败；重试后恢复', async () => {
    listFilesMock.mockRejectedValueOnce(new ApiError(500, 'internal', 'boom'))
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('文件列表加载失败')
    expect(w.text()).toContain('已下发 0 张') // 主状态不受文件列表失败影响
    listFilesMock.mockResolvedValue([{ name: 'a.png', size: 10 }])
    const retry = w.findAll('button').find((b) => b.text() === '重试')
    await retry?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('a.png')
    w.unmount()
  })

  it('主状态失败：文件列表仍正常显示，主状态区独立报错可重试', async () => {
    getRefsMock.mockRejectedValue(new ApiError(500, 'internal', 'down'))
    listFilesMock.mockResolvedValue([{ name: 'b.png', size: 20 }])
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    expect(w.findAll('[role="alert"]').length).toBeGreaterThan(0)
    expect(w.text()).toContain('b.png') // 文件列表不受主状态失败影响
    expect(w.text()).toContain('已下发 0 张')
    w.unmount()
  })

  it('HTTP 下发失败：错误可感知、记录 HTTP 失败快照，无未捕获异常', async () => {
    switchMock.mockRejectedValue(new ApiError(502, 'type14', 'vidu down'))
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    const draft = useRefsDraftStore()
    draft.add({ image_id: 'x', url: 'u' })
    await new Promise((r) => setTimeout(r, 0))
    await w.findAll('button').find((b) => b.text() === '下发切换')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('参考图指令失败')
    const snap = draft.snapshots[0]
    expect(snap.http).toBe('error')
    expect(snap.ack).toBe('failed')
    expect(w.text()).toContain('Vidu 回执失败')
    w.unmount()
  })

  it('HTTP 成功但 ack 失败：显示「已下发」而非「已应用」，可重试', async () => {
    switchMock.mockResolvedValue(
      stateFixture({ images: [{ image_id: 'x', url: 'u' }], ack: { success: false, error: 'type14', at_ms: 1 } })
    )
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    const draft = useRefsDraftStore()
    draft.add({ image_id: 'x', url: 'u' })
    await new Promise((r) => setTimeout(r, 0))
    await w.findAll('button').find((b) => b.text() === '下发切换')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('等待 Vidu 回执')
    expect(w.text()).not.toContain('已下发并已应用（')
    expect(draft.snapshots[0].ack).toBe('failed')
    // ack 失败重试
    switchMock.mockResolvedValue(
      stateFixture({ images: [{ image_id: 'x', url: 'u' }], ack: { success: true, at_ms: 2 } })
    )
    await w.findAll('button').find((b) => b.text() === '重试')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(draft.snapshots[0].ack).toBe('success')
    w.unmount()
  })

  it('URL 与资产 ID 输入在组件卸载重建后保留', async () => {
    const first = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    await first.find('input[placeholder="图片 URL"]').setValue('https://example.com/draft.png')
    await first.find('input[placeholder="资产 ID（可选）"]').setValue('draft-image')
    first.unmount()

    const second = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    expect((second.find('input[placeholder="图片 URL"]').element as HTMLInputElement).value).toBe('https://example.com/draft.png')
    expect((second.find('input[placeholder="资产 ID（可选）"]').element as HTMLInputElement).value).toBe('draft-image')
    second.unmount()
  })

  it('无旧快照不显示撤销；有两组后可恢复上一组', async () => {
    const w = mount(RefsPanel)
    await new Promise((r) => setTimeout(r, 0))
    const draft = useRefsDraftStore()
    expect(w.findAll('button').some((b) => b.text().includes('恢复上一组'))).toBe(false)
    switchMock
      .mockResolvedValueOnce(stateFixture({ images: [{ image_id: 'a', url: 'u1' }], ack: { success: true, at_ms: 1 } }))
      .mockResolvedValueOnce(stateFixture({ images: [{ image_id: 'b', url: 'u2' }], ack: { success: true, at_ms: 2 } }))
      .mockResolvedValueOnce(stateFixture({ images: [{ image_id: 'a', url: 'u1' }], ack: { success: true, at_ms: 3 } }))
    draft.add({ image_id: 'a', url: 'u1' })
    await new Promise((r) => setTimeout(r, 0))
    await w.findAll('button').find((b) => b.text() === '下发切换')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(w.findAll('button').some((b) => b.text().includes('恢复上一组'))).toBe(false) // 仅一组仍无撤销
    draft.add({ image_id: 'b', url: 'u2' })
    await new Promise((r) => setTimeout(r, 0))
    await w.findAll('button').find((b) => b.text() === '下发切换')?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    const undoBtn = w.findAll('button').find((b) => b.text().includes('恢复上一组'))
    expect(undoBtn).toBeDefined()
    await undoBtn?.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(switchMock).toHaveBeenCalledTimes(3)
    expect(switchMock.mock.calls[2][0]).toEqual([{ image_id: 'a', url: 'u1' }])
    expect(w.text()).toContain('已恢复上一组参考图')
    w.unmount()
  })
})
