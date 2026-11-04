import { defineStore } from 'pinia'
import {
  clearTranscript,
  clearActions,
  getLiveStatus,
  interrupt,
  preflightLive,
  resetLlm,
  say,
  sendCommand,
  startLive,
  stopActions,
  stopLive
} from '../services/api/live'
import { ApiError, classifyApiError } from '../services/api/client'
import type { LiveCheckStatus, LivePreflightResult } from '../services/api/live'
import type { LiveStatus, WireEvent } from '../services/api/types'

let loadVersion = 0
// 开播 in-flight 互斥：连点/双击只产生一个请求（配合后端幂等键）。
let startFlight: Promise<boolean> | null = null

interface StartInput {
  personaId?: string
  avatarId?: string
  greeting?: string
  draftId?: string
  expectedRevision?: number
}

type SelectionGuard = () => boolean | Promise<boolean>

const CHECK_STATUSES = new Set<LiveCheckStatus>(['pass', 'warn', 'fail', 'unknown', 'running'])

function requirePreflightBinding(result: LivePreflightResult): void {
  if (result.mode !== 'public-live' && result.mode !== 'local-preview') {
    throw new ApiError(428, 'preflight_contract_invalid', `开播预检返回非法 mode：${String(result.mode)}，已阻断开播`)
  }
  const invalidCheck = result.checks.find((c) => !CHECK_STATUSES.has(c.status))
  if (invalidCheck) {
    throw new ApiError(428, 'preflight_contract_invalid', `开播预检返回非法 check status：${invalidCheck.code}=${invalidCheck.status}，已阻断开播`)
  }
  const missing: string[] = []
  if (result.mode === 'public-live' && !result.personaId) missing.push('personaId')
  if (result.mode === 'public-live' && !result.draftId) missing.push('draftId')
  if (result.mode === 'public-live' && !result.draftRevision) missing.push('draftRevision')
  if (result.revision === undefined) missing.push('revision')
  if (!result.resultDigest) missing.push('resultDigest')
  if (!result.preflightToken) missing.push('preflightToken')
  // public-live 必须绑定 persona/draft；local-preview 允许两者为空，其余绑定字段两种模式均不可缺失。
  if (missing.length) {
    throw new ApiError(428, 'preflight_contract_missing', `开播预检缺少字段：${missing.join('、')}，已阻断开播`)
  }
}

function requireUnexpiredToken(result: LivePreflightResult): void {
  if (result.expiresAtMs <= Date.now()) throw new ApiError(428, 'preflight_required', '开播预检凭证已过期，请重新预检')
}

interface LiveState {
  status: LiveStatus | null
  busy: boolean
  controlling: boolean
  error: string
  errorCode: string
  lastLoadedAt: number
  llmClearedTurns: number
}

export const useLiveStore = defineStore('live', {
  state: (): LiveState => ({
    status: null,
    busy: false,
    controlling: false,
    error: '',
    errorCode: '',
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
      this.errorCode = ''
      try {
        await fn()
        return true
      } catch (err) {
        const c = classifyApiError(err)
        this.error = c.message
        this.errorCode = c.code
        return false
      } finally {
        this.busy = false
        await this.load()
      }
    },

    async start(input: StartInput = {}, selectionGuard?: SelectionGuard): Promise<boolean> {
      if (startFlight) return startFlight
      startFlight = this.stableAction(async () => {
        const draftId = input.draftId ?? ''
        const expectedRevision = input.expectedRevision ?? 0
        const preflight = await preflightLive({
          personaId: input.personaId ?? '',
          draftId,
          expectedRevision
        })
        requirePreflightBinding(preflight)
        const blocked = preflight.checks.filter((c) => c.status === 'fail' || c.status === 'unknown' || c.status === 'running')
        if (blocked.some((c) => c.detail?.includes('[publish_target_unverified]'))) {
          throw new ApiError(428, 'publish_target_unverified', 'publish_target_unverified')
        }
        if (!preflight.ready || blocked.length) {
          const details = blocked.map((c) => `${c.code}=${c.status}${c.detail ? `：${c.detail}` : ''}`).join('；')
          throw new ApiError(428, 'preflight_not_ready', `开播预检未通过${details ? `：${details}` : ''}`)
        }
        requireUnexpiredToken(preflight)
        if (selectionGuard && !(await selectionGuard())) {
          throw new ApiError(428, 'preflight_required', '预检后档案或版本已变化，请重新预检')
        }
        requireUnexpiredToken(preflight)
        await startLive({
          personaId: preflight.personaId,
          avatarId: input.avatarId,
          greeting: input.greeting,
          sessionId: this.status?.sessionId ?? '',
          draftId: preflight.draftId,
          revision: preflight.revision!,
          expectedRevision: preflight.draftRevision,
          resultDigest: preflight.resultDigest,
          preflightToken: preflight.preflightToken
        })
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
