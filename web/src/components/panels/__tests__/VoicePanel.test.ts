import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const h = vi.hoisted(() => ({
  getVoices: vi.fn(),
  listTTSFiles: vi.fn(),
  setVoice: vi.fn(),
  previewVoice: vi.fn()
}))

vi.mock('../../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/panels')>('../../../services/api/panels')
  return {
    ...actual,
    getVoices: h.getVoices,
    listTTSFiles: h.listTTSFiles,
    setVoice: h.setVoice,
    previewVoice: h.previewVoice
  }
})

import VoicePanel from '../VoicePanel.vue'
import NtConfirmDialog from '../../ui/NtConfirmDialog.vue'

describe('VoicePanel 音色作用域', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    h.getVoices.mockResolvedValue({ voices: [{ name: 'voice-next', label: '下一场音色' }], current: 'voice-current', active_base: 'base' })
    h.listTTSFiles.mockResolvedValue({ files: [], dir: '' })
    h.setVoice.mockResolvedValue(undefined)
  })

  it('直播中冻结当场音色，不调用即时切换接口', async () => {
    const w = mount(VoicePanel, { props: { voiceId: 'voice-current', liveMode: true } })
    await flushPromises()

    expect(w.find('[data-testid="voice-scope"]').text()).toContain('当场音色已冻结')
    expect(w.find('[data-testid="voice-scope"]').text()).toContain('下一场')
    const item = w.find('.item')
    expect(item.attributes('disabled')).toBeDefined()
    await item.trigger('click')
    expect(h.setVoice).not.toHaveBeenCalled()
    w.unmount()
  })

  it('准备态切换只确认接口与下一场用途，不再回写设置草稿', async () => {
    const w = mount(VoicePanel, { props: { voiceId: 'voice-current', liveMode: false } })
    await flushPromises()

    await w.find('.item').trigger('click')
    await flushPromises()
    expect(h.setVoice).toHaveBeenCalledWith('voice-next')
    expect(w.emitted('select')).toBeUndefined()
    expect(w.text()).toContain('音色接口已生效')
    expect(w.text()).toContain('下一场')
    w.unmount()
  })

  it('fake 音色库禁用真实试听并显示原因', async () => {
    h.getVoices.mockResolvedValue({
      voices: [{ name: 'voice-fake', label: '开发预览音色' }],
      current: 'voice-fake',
      active_base: 'fake-omnivoice://local'
    })
    const w = mount(VoicePanel, { props: { voiceId: 'voice-fake' } })
    await flushPromises()

    const preview = w.find('.preview')
    expect(preview.attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid="voice-capability"]').text()).toContain('真实试听不可用')
    await preview.trigger('click')
    expect(h.previewVoice).not.toHaveBeenCalled()
    w.unmount()
  })

  it('语音服务未配置时禁用试听并显示配置提示', async () => {
    h.getVoices.mockResolvedValue({
      voices: [],
      current: '',
      active_base: 'unavailable:tts_unavailable'
    })
    const w = mount(VoicePanel, { props: { voiceId: '' } })
    await flushPromises()

    const preview = w.find('.preview')
    expect(preview.attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('语音合成服务尚未配置')
    await preview.trigger('click')
    expect(h.previewVoice).not.toHaveBeenCalled()
    w.unmount()
  })

  it('不可恢复删除音频使用危险确认合同', async () => {
    h.listTTSFiles.mockResolvedValue({ files: [{ name: 'sample.wav', size: 1024 }], dir: '/tmp' })
    const w = mount(VoicePanel, { props: { voiceId: 'voice-current' } })
    await flushPromises()

    await w.find('.flist .mini').trigger('click')
    const dialog = w.findComponent(NtConfirmDialog)
    expect(dialog.props('open')).toBe(true)
    expect(dialog.props('dangerous')).toBe(true)
    w.unmount()
  })
})
