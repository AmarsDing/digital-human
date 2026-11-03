import { defineStore } from 'pinia'
import {
  clearTranscript,
  clearActions,
  getLiveStatus,
  interrupt,
  resetLlm,
  say,
  sendCommand,
  startLive,
  stopActions,
  stopLive
} from '../services/api/live'
import { classifyApiError } from '../services/api/client'
import type { LiveStatus, WireEvent } from '../services/api/types'

let loadVersion = 0
// 开播 in-flight 互斥：连点/双击只产生一个请求（配合后端幂等键）。
let startFlight: Promise<boolean> | null = null

interface LiveState {
  status: LiveStatus | null
  busy: boolean
  controlling: boolean
  error: string
  lastLoadedAt: number
  llmClearedTurns: number
}

export const useLiveStore = defineStore('live', {
  state: (): LiveState => ({
    status: null,
    busy: false,
    controlling: false,
    error: '',
    lastLoadedAt: 0,
    llmClearedTurns: 0
  }),
  getters: {
    phase: (s) => s.status?.phase || 'idle',
    isLive: (s) => s.status?.phase === 'live',
    durationS: (s) => s.status?.durationS ?? 0,
    lagMs: (s) => s.status?.feed?.lagMs ?? 0,
    feedOwner: (s) => !!s.status?.feed?.owner,
    wsState: (s) => s.status?.ws?.state || '',
    tokenExpireAt: (s) => s.status?.rtc?.tokenExpireAt || 0,
    rtcReady: (s) => !!s.status?.rtc?.tokenExpireAt
  },
  actions: {
    async load(): Promise<void> {
      const version = ++loadVersion
      try {
        const status = await getLiveStatus()
        if (version !== loadVersion) return // 慢响应不覆盖新状态（R-UI-35）
        this.status = status
        this.lastLoadedAt = Date.now()
      } catch (err) {
        if (version !== loadVersion) return
        this.error = classifyApiError(err).message
      }
    },

    async stableAction(fn: () => Promise<void>): Promise<boolean> {
      this.busy = true
      this.error = ''
      try {
        await fn()
        return true
      } catch (err) {
        this.error = classifyApiError(err).message
        return false
      } finally {
        this.busy = false
        await this.load()
      }
    },

    async start(input: { personaId?: string; avatarId?: string; greeting?: string } = {}): Promise<boolean> {
      if (startFlight) return startFlight
      startFlight = this.stableAction(async () => {
        await startLive({ ...input, sessionId: this.status?.sessionId ?? '' })
      }).finally(() => {
        startFlight = null
      })
      return startFlight
    },

    async stop(reason = 'manual'): Promise<boolean> {
      return this.stableAction(async () => {
        await stopLive(reason)
      })
    },

    async say(text: string, mode: 'sentence' | 'whole' = 'sentence'): Promise<boolean> {
      return this.stableAction(async () => {
        await say(text, mode)
      })
    },

    async command(text: string): Promise<boolean> {
      return this.stableAction(async () => {
        await sendCommand(text)
      })
    },

    async interrupt(): Promise<boolean> {
      return this.stableAction(async () => {
        await interrupt()
      })
    },

    async clearTranscripts(): Promise<boolean> {
      return this.stableAction(async () => {
        await clearTranscript()
      })
    },

    async stopCurrentActions(): Promise<boolean> {
      return this.stableAction(async () => {
        await stopActions()
      })
    },

    async clearAllActions(): Promise<boolean> {
      return this.stableAction(async () => {
        await clearActions()
      })
    },

    async resetLlmMemory(): Promise<boolean> {
      const sessionId = this.status?.sessionId || ''
      return this.stableAction(async () => {
        const reply = await resetLlm(sessionId)
        this.llmClearedTurns = reply.clearedTurns
      })
    },

    // WS 事件派生：只更新展示状态，不反向触发请求。
    applyEvent(e: WireEvent): void {
      switch (e.type) {
        case 'live.start_requested':
          if (this.status) this.status.phase = 'starting'
          break
        case 'live.started':
          if (this.status) {
            this.status.phase = 'live'
            this.status.liveId = String(e.payload?.live_id ?? this.status.liveId)
          }
          break
        case 'live.start_failed':
          if (this.status) {
            this.status.phase = 'error'
            this.status.errorCode = 'engine_start_failed'
            this.status.errorMessage = String(e.payload?.error ?? '')
          }
          break
        case 'live.stopped':
          if (this.status) this.status.phase = 'ended'
          break
        case 'live.crash_recovered':
          if (this.status) {
            this.status.phase = 'error'
            this.status.errorCode = 'crash_recovered'
          }
          break
        default:
          break
      }
    }
  }
})
