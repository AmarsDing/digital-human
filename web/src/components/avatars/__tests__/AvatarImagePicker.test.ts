import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { resetBlobPool } from '../../../services/blob-pool'

const h = vi.hoisted(() => ({
  uploadAvatarImage: vi.fn(),
  fetchAvatarImage: vi.fn()
}))

vi.mock('../../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/panels')>('../../../services/api/panels')
  return {
    ...actual,
    uploadAvatarImage: (...args: unknown[]) => h.uploadAvatarImage(...args),
    fetchAvatarImage: (...args: unknown[]) => h.fetchAvatarImage(...args)
  }
})

import AvatarImagePicker from '../AvatarImagePicker.vue'

// R3-03 回归：await checkPixels 窗口期连选两文件——进入处理流程即禁用，
// 第二文件被拒且有 role=alert 提示，不允许静默吞掉后图文不符。
// R3-06 回归：校验失败清 lastFile，「重试上传」只对上传失败开放。
class FakeImage {
  naturalWidth = 800
  naturalHeight = 800
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  set src(_v: string) {
    queueMicrotask(() => this.onload?.())
  }
}

function png(name = 'a.png'): File {
  return new File(['x'], name, { type: 'image/png' })
}

async function selectFile(w: VueWrapper, f: File): Promise<void> {
  const input = w.find('input[type="file"]').element as HTMLInputElement
  Object.defineProperty(input, 'files', { value: [f], configurable: true })
  input.dispatchEvent(new Event('change'))
  await flushPromises()
}

// 选择/更换/上传中按钮（首个 button 是缩略图，需按文案定位）。
function pickBtn(w: VueWrapper) {
  return w.findAll('button').find((b) => ['选择图片', '更换图片', '上传中…'].includes(b.text()))
}

describe('AvatarImagePicker 上传窗口期竞争（R3-03）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    h.fetchAvatarImage.mockRejectedValue(new Error('no image'))
    vi.stubGlobal('Image', FakeImage)
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: vi.fn(() => 'blob:mock'),
      revokeObjectURL: vi.fn()
    })
    // 须在 URL stub 之后清池：上个用例遗留 blob 会走 revokeObjectURL。
    resetBlobPool()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('窗口期连选第二文件被拒并提示，仅第一份上传', async () => {
    let resolveUpload: ((v: { asset_id: string }) => void) | null = null
    h.uploadAvatarImage.mockImplementation(
      () => new Promise((res) => { resolveUpload = res })
    )
    const w = mount(AvatarImagePicker, { props: { personaId: 'p1' } })
    await flushPromises()

    await selectFile(w, png('a.png'))
    // 第一份进入上传中：选择按钮禁用
    expect(pickBtn(w)!.attributes('disabled')).toBeDefined()
    expect(pickBtn(w)!.text()).toBe('上传中…')

    // 窗口期连选第二文件
    await selectFile(w, png('b.png'))
    expect(h.uploadAvatarImage).toHaveBeenCalledTimes(1)
    const alert = w.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('请稍候再试')
    expect(w.emitted('uploaded')).toBeUndefined()

    // 第一份完成：emit 的是第一份 asset_id，按钮恢复可用
    resolveUpload!({ asset_id: 'asset-a' })
    await flushPromises()
    expect(w.emitted('uploaded')).toEqual([['asset-a']])
    expect(pickBtn(w)!.attributes('disabled')).toBeUndefined()
  })

  it('校验失败不出现「重试上传」（R3-06），且选择按钮恢复可用', async () => {
    const w = mount(AvatarImagePicker, { props: { personaId: 'p1' } })
    await flushPromises()
    await selectFile(w, new File(['x'], 'a.gif', { type: 'image/gif' }))
    expect(w.find('[role="alert"]').text()).toContain('仅支持 PNG / JPEG')
    expect(w.findAll('button').some((b) => b.text() === '重试上传')).toBe(false)
    expect(pickBtn(w)!.attributes('disabled')).toBeUndefined()
    expect(h.uploadAvatarImage).not.toHaveBeenCalled()
  })

  it('上传失败出现「重试上传」，点击后重传同一文件', async () => {
    h.uploadAvatarImage
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce({ asset_id: 'asset-a' })
    const w = mount(AvatarImagePicker, { props: { personaId: 'p1' } })
    await flushPromises()
    await selectFile(w, png())
    expect(w.find('[role="alert"]').text()).toContain('boom')
    const retry = w.findAll('button').find((b) => b.text() === '重试上传')
    expect(retry).toBeTruthy()
    await retry!.trigger('click')
    await flushPromises()
    expect(h.uploadAvatarImage).toHaveBeenCalledTimes(2)
    expect(w.emitted('uploaded')).toEqual([['asset-a']])
  })
})
