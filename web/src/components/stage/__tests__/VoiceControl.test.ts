import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import VoiceControl from '../VoiceControl.vue'

const h = vi.hoisted(() => ({ useVoiceInteraction: vi.fn() }))

vi.mock('../../../services/voice/useVoiceInteraction', () => ({
  useVoiceInteraction: h.useVoiceInteraction
}))

function voiceFixture() {
  return {
    mode: ref<'ptt' | 'continuous'>('ptt'),
    phase: ref<'idle' | 'connecting' | 'listening' | 'thinking' | 'speaking' | 'error'>('idle'),
    error: ref(''),
    latencyText: ref(''),
    sourceText: ref(''),
    micDisabled: ref(false),
    refresh: vi.fn().mockResolvedValue(undefined),
    press: vi.fn().mockResolvedValue(undefined),
    release: vi.fn().mockResolvedValue(undefined),
    cancel: vi.fn().mockResolvedValue(undefined)
  }
}

function mountControl() {
  return mount(VoiceControl, { props: { sessionId: 's-1', epoch: 7, canSpeak: true } })
}

describe('VoiceControl', () => {
  beforeEach(() => {
    h.useVoiceInteraction.mockReset()
    h.useVoiceInteraction.mockReturnValue(voiceFixture())
  })

  it('按住说话按下开始、松开结束，并加载真实能力状态', async () => {
    const voice = voiceFixture()
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    await flushPromises()
    expect(voice.refresh).toHaveBeenCalled()
    expect(h.useVoiceInteraction).toHaveBeenCalledWith(expect.objectContaining({
      sessionId: expect.any(Function), epoch: expect.any(Function), canSpeak: expect.any(Function)
    }))
    const mic = w.get('button[aria-label="按住说话"]')
    await mic.trigger('pointerdown', { pointerId: 1 })
    await mic.trigger('pointerup', { pointerId: 1 })
    expect(voice.press).toHaveBeenCalledTimes(1)
    expect(voice.release).toHaveBeenCalledTimes(1)
  })

  it('按住说话在按钮外松开也会结束回合', async () => {
    const voice = voiceFixture()
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    await w.get('button[aria-label="按住说话"]').trigger('pointerdown', { pointerId: 1 })
    window.dispatchEvent(new Event('pointerup'))
    expect(voice.release).toHaveBeenCalledTimes(1)
    w.unmount()
  })

  it('连续对话模式通过点击开始和停止', async () => {
    const voice = voiceFixture()
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    await w.get('button[aria-label="切换连续对话"]').trigger('click')
    expect(voice.mode.value).toBe('continuous')
    await w.get('button[aria-label="开始连续对话"]').trigger('click')
    voice.phase.value = 'listening'
    await w.get('button[aria-label="停止连续对话"]').trigger('click')
    expect(voice.press).toHaveBeenCalledTimes(1)
    expect(voice.cancel).toHaveBeenCalled()
  })

  it.each([
    ['listening', '听'],
    ['thinking', '想'],
    ['speaking', '说']
  ] as const)('%s 状态只显示轻量指示 %s', async (phase, label) => {
    const voice = voiceFixture()
    voice.phase.value = phase
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    expect(w.get('[aria-live="polite"]').text()).toContain(label)
  })

  it('如实显示能力错误、fake 边界与首音频延迟', async () => {
    const voice = voiceFixture()
    voice.phase.value = 'error'
    voice.error.value = '语音识别不可用，请改用文字输入'
    voice.sourceText.value = 'fake 验证模式'
    voice.latencyText.value = '首音频 1234ms'
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    expect(w.get('[role="alert"]').text()).toContain('语音识别不可用')
    expect(w.text()).toContain('fake 验证模式')
    expect(w.text()).toContain('首音频 1234ms')
  })

  it('场次切换时取消旧回合并重新读取能力', async () => {
    const voice = voiceFixture()
    h.useVoiceInteraction.mockReturnValue(voice)
    const w = mountControl()
    await w.setProps({ sessionId: 's-2', epoch: 8 })
    await flushPromises()
    expect(voice.cancel).toHaveBeenCalled()
    expect(voice.refresh).toHaveBeenCalledTimes(2)
  })
})
