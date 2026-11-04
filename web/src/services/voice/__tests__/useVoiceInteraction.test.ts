import { describe, expect, it, vi } from 'vitest'
import { useVoiceInteraction } from '../useVoiceInteraction'
import type { VoiceCapture, VoiceStatus, VoiceTransport } from '../types'

function readyStatus(): VoiceStatus {
  return {
    ready: true,
    asr: { source: 'real', ready: true },
    llm: { source: 'unknown', ready: true },
    tts: { source: 'real', ready: true },
    live: { source: 'unknown', ready: true },
    default_mode: 'ptt', continuous: true, audio_retained: false, interrupt_songs: false, max_audio_ms: 20000
  }
}

function fixture(overrides: { status?: VoiceStatus; captureError?: Error; browserIssue?: string } = {}) {
  const transport: VoiceTransport = {
    connect: vi.fn().mockResolvedValue(undefined), begin: vi.fn(), sendPCM: vi.fn(),
    end: vi.fn(), cancel: vi.fn(), close: vi.fn()
  }
  const capture: VoiceCapture = {
    start: overrides.captureError ? vi.fn().mockRejectedValue(overrides.captureError) : vi.fn().mockResolvedValue(undefined),
    stop: vi.fn().mockResolvedValue(undefined)
  }
  let eventHandler: (event: import('../types').VoiceServerEvent) => void = () => undefined
  const voice = useVoiceInteraction({
    sessionId: () => 's-1', epoch: () => 3, canSpeak: () => true,
    statusLoader: () => Promise.resolve(overrides.status ?? readyStatus()),
    transportFactory: (options) => {
      eventHandler = options.onEvent
      return transport
    },
    captureFactory: () => capture,
    browserIssue: () => overrides.browserIssue ?? ''
  })
  return { voice, transport, capture, emit: (event: import('../types').VoiceServerEvent) => eventHandler(event) }
}

describe('useVoiceInteraction', () => {
  it('ASR 不可用时 fail-closed，不创建连接或采集', async () => {
    const status = readyStatus()
    status.ready = false
    status.asr = { source: 'unconfigured', ready: false, error_code: 'asr_unavailable' }
    const { voice, transport } = fixture({ status })
    await voice.refresh()
    await voice.press()
    expect(voice.phase.value).toBe('error')
    expect(voice.error.value).toContain('语音识别不可用')
    expect(transport.connect).not.toHaveBeenCalled()
    expect(voice.micDisabled.value).toBe(true)
  })

  it('PTT 按下开始独立回合，松开停止采集并 end', async () => {
    const { voice, transport, capture, emit } = fixture()
    await voice.refresh()
    await voice.press()
    expect(transport.connect).toHaveBeenCalledWith('s-1', expect.any(String))
    expect(transport.begin).toHaveBeenCalledWith(3, 'ptt', expect.any(String))
    expect(capture.start).toHaveBeenCalled()
    emit({ type: 'accepted', stage: 'listening', turn_id: 'turn-a' })
    expect(voice.phase.value).toBe('listening')
    await voice.release()
    expect(capture.stop).toHaveBeenCalled()
    expect(transport.end).toHaveBeenCalled()
  })

  it('后端禁用连续对话时 fail-closed，不连接或采集', async () => {
    const status = readyStatus()
    status.continuous = false
    const { voice, transport, capture } = fixture({ status })
    voice.mode.value = 'continuous'
    await voice.refresh()
    await voice.press()
    expect(voice.phase.value).toBe('error')
    expect(voice.error.value).toContain('连续对话未启用')
    expect(voice.micDisabled.value).toBe(true)
    expect(transport.connect).not.toHaveBeenCalled()
    expect(capture.start).not.toHaveBeenCalled()
  })

  it('连续对话完成后在同一语音 WS 开始下一回合', async () => {
    const { voice, transport, capture, emit } = fixture()
    voice.mode.value = 'continuous'
    await voice.refresh()
    await voice.press()
    emit({ type: 'voice.completed', turn_id: 'turn-a', reason: 'completed', timings: {} })
    expect(transport.begin).toHaveBeenCalledTimes(2)
    expect(transport.close).not.toHaveBeenCalled()
    expect(capture.stop).not.toHaveBeenCalled()
    await voice.cancel()
    expect(transport.cancel).toHaveBeenCalled()
    expect(transport.close).toHaveBeenCalled()
  })

  it('听想说状态与首音频延迟来自后端事件', async () => {
    const { voice, emit } = fixture()
    await voice.refresh()
    await voice.press()
    emit({ type: 'voice.thinking', turn_id: 'turn-a', timings: { speech_end_at_ms: 10 } })
    expect(voice.phase.value).toBe('thinking')
    emit({ type: 'voice.speaking', turn_id: 'turn-a', timings: { speech_end_at_ms: 10, llm_first_token_at_ms: 20 } })
    expect(voice.phase.value).toBe('speaking')
    emit({ type: 'voice.first_audio', turn_id: 'turn-a', timings: { speech_end_to_first_audio_ms: 1234 } })
    expect(voice.latencyText.value).toBe('首音频 1234ms')
  })

  it('麦克风权限拒绝时给出明确降级提示并取消连接', async () => {
    const denied = new DOMException('denied', 'NotAllowedError')
    const { voice, transport } = fixture({ captureError: denied })
    await voice.refresh()
    await voice.press()
    expect(voice.phase.value).toBe('error')
    expect(voice.error.value).toContain('麦克风权限被拒绝')
    expect(voice.error.value).toContain('文字输入')
    expect(transport.cancel).toHaveBeenCalled()
    expect(transport.close).toHaveBeenCalled()
  })

  it('非安全上下文明确说明 HTTPS/localhost 限制', async () => {
    const { voice, transport } = fixture({ browserIssue: '麦克风采集需要 HTTPS 或 localhost；仍可使用文字输入' })
    await voice.refresh()
    await voice.press()
    expect(voice.error.value).toContain('HTTPS')
    expect(transport.connect).not.toHaveBeenCalled()
  })

  it('fake 能力如实展示，不伪装成真实模型', async () => {
    const status = readyStatus()
    status.asr.source = 'fake'
    status.tts.source = 'fake'
    const { voice } = fixture({ status })
    await voice.refresh()
    expect(voice.sourceText.value).toBe('fake 验证模式')
  })
})
