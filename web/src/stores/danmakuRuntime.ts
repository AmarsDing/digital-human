import { defineStore } from 'pinia'
import { classifyApiError } from '../services/api/client'
import { createVisibilityPolling, type VisibilityPolling } from '../utils/polling'
import {
  confirmDanmaku,
  dismissDanmaku,
  getDanmakuState,
  listDanmakuPending,
  setDanmakuMode,
  type DanmakuPendingItem,
  type DanmakuState
} from '../services/api/panels'

// 弹幕运行态（v2 §2.4）：Stage 级唯一所有者，组件只是选择器。
// 唯一 timer：任何时刻最多一个轮询；document.hidden 暂停，恢复先立即刷新再续同一个调度。
// 断网显示持久错误与最后成功时间，pendingCount 不伪报 0。
let poll: VisibilityPolling | null = null

type DanmakuMode = 'off' | 'confirm' | 'auto'

interface DanmakuRuntimeState {
  mode: DanmakuMode
  enabled: boolean
  pending: DanmakuPendingItem[]
  recent: string[]
  top: { text: string; count: number }[]
  requestError: string
  lastSuccessAt: number
  busy: boolean
  // 唯一调度运行标记（响应式）：start/stop 维护，用于「卸载组件 timer 仍唯一」可观测。
  running: boolean
  // 本地模式选择未被服务端接受前为 true；Badge 仍显示服务端有效值，不假装成功。
  modeApplying: boolean
  note: string
}

export const useDanmakuRuntimeStore = defineStore('danmakuRuntime', {
  state: (): DanmakuRuntimeState => ({
    mode: 'off',
    enabled: false,
    pending: [],
    recent: [],
    top: [],
    requestError: '',
    lastSuccessAt: 0,
    busy: false,
    running: false,
    modeApplying: false,
    note: ''
  }),
  getters: {
    pendingCount: (s) => s.pending.length
  },
  actions: {
    // Stage 挂载后启动唯一调度；重复调用不新建第二个 timer。
    start(): void {
      if (poll?.running()) return
      poll?.stop()
      poll = createVisibilityPolling(() => void this.load(), 3000)
      poll.start()
      this.running = true
      void this.load()
    },

    // Stage 离场/销毁时清理 timer。
    stop(): void {
      poll?.stop()
      poll = null
      this.running = false
    },

    async load(): Promise<void> {
      try {
        const r = await getDanmakuState()
        const state: DanmakuState = r.state
        const items = await listDanmakuPending()
        if (!this.modeApplying) {
          this.mode = (state.mode as DanmakuMode) || 'off'
        }
        this.enabled = Boolean(state.enabled)
        this.recent = state.recent ?? []
        this.top = state.top ?? []
        this.pending = items
        this.requestError = ''
        this.lastSuccessAt = Date.now()
      } catch (err) {
        // 持久错误：保留 pending/lastSuccessAt，不伪报 0、不伪造成功时间。
        this.requestError = classifyApiError(err).message
      }
    },

    async applyMode(mode: DanmakuMode): Promise<void> {
      if (this.busy) return
      this.busy = true
      this.modeApplying = true
      this.requestError = ''
      this.note = ''
      try {
        const r = await setDanmakuMode(mode)
        this.note = r.changed ? `模式已切换为 ${r.mode}` : `模式未变（${r.mode}）`
        this.mode = (r.mode as DanmakuMode) || mode
      } catch (err) {
        this.requestError = classifyApiError(err).message
      } finally {
        this.modeApplying = false
        this.busy = false
        await this.load()
      }
    },

    async decide(id: string, ok: boolean): Promise<void> {
      if (this.busy) return
      this.busy = true
      this.requestError = ''
      this.note = ''
      try {
        if (ok) {
          const r = await confirmDanmaku(id)
          if (!r.ok) throw new Error(r.reason || '确认失败')
          this.note = '已确认并进入播报队列'
        } else {
          const r = await dismissDanmaku(id)
          if (!r.ok) throw new Error('忽略失败')
          this.note = '已忽略该条弹幕'
        }
        this.pending = await listDanmakuPending()
        this.lastSuccessAt = Date.now()
      } catch (err) {
        // 先以服务端为准重读，再透出本次操作错误（避免被重读成功清掉）。
        const msg = classifyApiError(err).message
        await this.load().catch(() => undefined)
        this.requestError = msg
      } finally {
        this.busy = false
      }
    }
  }
})
