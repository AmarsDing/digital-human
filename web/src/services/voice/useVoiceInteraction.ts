import { computed, getCurrentScope, onScopeDispose, ref } from 'vue'
import { getVoiceStatus } from '../api/voice'
import { PcmCapture, voiceBrowserIssue } from './PcmCapture'
import { VoiceSocket } from './VoiceSocket'
import type {
  VoiceCapture,
  VoiceMode,
  VoicePhase,
  VoiceServerEvent,
  VoiceStatus,
  VoiceTransport,
  VoiceTransportOptions
} from './types'

export interface VoiceInteractionOptions {
  sessionId: () => string
  epoch: () => number
  canSpeak: () => boolean
  statusLoader?: () => Promise<VoiceStatus>
  transportFactory?: (options: VoiceTransportOptions) => VoiceTransport
  captureFactory?: () => VoiceCapture
  browserIssue?: () => string
}

export function useVoiceInteraction(options: VoiceInteractionOptions) {
  const statusLoader = options.statusLoader ?? getVoiceStatus
  const transportFactory = options.transportFactory ?? ((transportOptions: VoiceTransportOptions) => new VoiceSocket(transportOptions))
  const captureFactory = options.captureFactory ?? (() => new PcmCapture())
  const browserIssue = options.browserIssue ?? voiceBrowserIssue

  const mode = ref<VoiceMode>('ptt')
  const phase = ref<VoicePhase>('idle')
  const error = ref('')
  const latencyText = ref('')
  const status = ref<VoiceStatus | null>(null)
  const sourceText = computed(() => {
    const current = status.value
    if (!current) return ''
    return [current.asr, current.llm, current.tts, current.live].some((capability) => capability.source === 'fake')
      ? 'fake 验证模式'
      : ''
  })
  const micDisabled = computed(() => {
    const current = status.value
    if (!current?.ready || !current.asr.ready || !current.tts.ready) return true
    return (mode.value === 'continuous' && !current.continuous) || !options.canSpeak() || Boolean(browserIssue())
  })

  const clientId = `voice-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  let generation = 0
  let refreshGeneration = 0
  let turnSequence = 0
  let active = false
  let releaseRequested = false
  let captureStarted = false
  let transport: VoiceTransport | null = null
  let capture: VoiceCapture | null = null

  function capabilityError(): string {
    const current = status.value
    if (current && !current.asr.ready) return '语音识别不可用，请检查语音识别配置或改用文字输入'
    if (current && !current.tts.ready) return '语音合成不可用，请检查语音合成配置或改用文字输入'
    if (current && mode.value === 'continuous' && !current.continuous) return '连续对话未启用，请切换按住说话或使用文字输入'
    return '语音服务尚未就绪，请使用文字输入'
  }

  function interactionError(cause: unknown): string {
    if (cause instanceof DOMException) {
      if (cause.name === 'NotAllowedError') return '麦克风权限被拒绝，请允许麦克风权限或改用文字输入'
      if (cause.name === 'NotFoundError') return '未找到可用麦克风，请检查设备或改用文字输入'
      if (cause.name === 'NotReadableError') return '麦克风无法读取或已被占用，请检查设备或改用文字输入'
      if (cause.name === 'SecurityError') return '麦克风采集需要 HTTPS 或 localhost；仍可使用文字输入'
    }
    if (cause instanceof Error && cause.message) return cause.message
    return '语音交互失败，请使用文字输入'
  }

  function closeTransport(current: VoiceTransport | null, sendCancel: boolean): void {
    if (!current) return
    if (sendCancel) {
      try {
        current.cancel()
      } catch {
        // 连接已失败时仍须关闭底层资源。
      }
    }
    current.close()
  }

  async function stopCapture(current: VoiceCapture | null): Promise<void> {
    if (current) await current.stop().catch(() => undefined)
  }

  function nextTurnId(): string {
    turnSequence += 1
    return `${clientId}-turn-${turnSequence}`
  }

  async function fail(currentGeneration: number, cause: unknown): Promise<void> {
    if (currentGeneration !== generation) return
    const currentTransport = transport
    const currentCapture = capture
    generation += 1
    active = false
    releaseRequested = false
    captureStarted = false
    transport = null
    capture = null
    phase.value = 'error'
    error.value = interactionError(cause)
    latencyText.value = ''
    closeTransport(currentTransport, true)
    await stopCapture(currentCapture)
  }

  async function complete(currentGeneration: number): Promise<void> {
    if (currentGeneration !== generation) return
    const currentTransport = transport
    const currentCapture = capture
    generation += 1
    active = false
    releaseRequested = false
    captureStarted = false
    transport = null
    capture = null
    phase.value = 'idle'
    closeTransport(currentTransport, false)
    await stopCapture(currentCapture)
  }

  function handleEvent(currentGeneration: number, event: VoiceServerEvent): void {
    if (currentGeneration !== generation || !active) return
    if (event.stage === 'listening' || event.stage === 'thinking' || event.stage === 'speaking') {
      phase.value = event.stage
    }
    if (event.type === 'voice.thinking') phase.value = 'thinking'
    if (event.type === 'voice.speaking') phase.value = 'speaking'
    if (event.type === 'voice.first_audio') {
      const latency = event.timings?.speech_end_to_first_audio_ms
      if (typeof latency === 'number') latencyText.value = `首音频 ${latency}ms`
    }
    if (event.type === 'voice.completed') {
      if (mode.value === 'continuous' && transport) {
        latencyText.value = ''
        try {
          transport.begin(options.epoch(), 'continuous', nextTurnId())
        } catch (cause) {
          void fail(currentGeneration, cause)
        }
      } else {
        void complete(currentGeneration)
      }
      return
    }
    if (event.type === 'rejected' || event.type === 'error' || event.type === 'voice.error') {
      void fail(currentGeneration, new Error(event.message || event.error || '语音服务拒绝了本次回合，请使用文字输入'))
    }
  }

  async function finishRelease(currentGeneration: number): Promise<void> {
    if (currentGeneration !== generation || !captureStarted) return
    const currentTransport = transport
    const currentCapture = capture
    if (!currentTransport || !currentCapture) return
    try {
      await currentCapture.stop()
      if (currentGeneration !== generation) return
      captureStarted = false
      currentTransport.end()
    } catch (cause) {
      await fail(currentGeneration, cause)
    }
  }

  async function refresh(): Promise<void> {
    const currentRefresh = ++refreshGeneration
    try {
      const next = await statusLoader()
      if (currentRefresh !== refreshGeneration) return
      status.value = next
      if (!active) {
        error.value = ''
        if (phase.value === 'error') phase.value = 'idle'
      }
    } catch {
      if (currentRefresh !== refreshGeneration) return
      status.value = null
      if (!active) {
        phase.value = 'error'
        error.value = '语音能力状态读取失败，请使用文字输入'
      }
    }
  }

  async function press(): Promise<void> {
    if (active) return
    const issue = browserIssue()
    if (issue) {
      phase.value = 'error'
      error.value = issue
      return
    }
    if (!options.canSpeak()) {
      phase.value = 'error'
      error.value = '当前状态不可语音输入，请使用文字输入'
      return
    }
    const currentStatus = status.value
    if (!currentStatus?.ready || !currentStatus.asr.ready || !currentStatus.tts.ready ||
      (mode.value === 'continuous' && !currentStatus.continuous)) {
      phase.value = 'error'
      error.value = capabilityError()
      return
    }

    const currentGeneration = ++generation
    active = true
    releaseRequested = false
    captureStarted = false
    phase.value = 'connecting'
    error.value = ''
    latencyText.value = ''
    try {
      const currentTransport = transportFactory({
        onEvent: (event) => handleEvent(currentGeneration, event),
        onClose: (intentional) => {
          if (!intentional && currentGeneration === generation && active) {
            void fail(currentGeneration, new Error('语音连接已断开，请重试或改用文字输入'))
          }
        }
      })
      const currentCapture = captureFactory()
      transport = currentTransport
      capture = currentCapture
      await currentTransport.connect(options.sessionId(), clientId)
      if (currentGeneration !== generation) return
      currentTransport.begin(options.epoch(), mode.value, nextTurnId())
      await currentCapture.start((pcm) => {
        if (currentGeneration !== generation || !active || releaseRequested) return
        try {
          currentTransport.sendPCM(pcm)
        } catch (cause) {
          void fail(currentGeneration, cause)
        }
      })
      if (currentGeneration !== generation) {
        await stopCapture(currentCapture)
        return
      }
      captureStarted = true
      if (releaseRequested) await finishRelease(currentGeneration)
    } catch (cause) {
      await fail(currentGeneration, cause)
    }
  }

  async function release(): Promise<void> {
    if (!active || mode.value !== 'ptt' || releaseRequested) return
    releaseRequested = true
    await finishRelease(generation)
  }

  async function cancel(): Promise<void> {
    const currentTransport = transport
    const currentCapture = capture
    generation += 1
    active = false
    releaseRequested = false
    captureStarted = false
    transport = null
    capture = null
    phase.value = 'idle'
    error.value = ''
    latencyText.value = ''
    closeTransport(currentTransport, true)
    await stopCapture(currentCapture)
  }

  if (getCurrentScope()) onScopeDispose(() => void cancel())

  return { mode, phase, error, latencyText, sourceText, micDisabled, refresh, press, release, cancel }
}
