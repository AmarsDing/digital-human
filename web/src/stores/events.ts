import { defineStore } from 'pinia'
import { wsEventsUrl } from '../services/api/endpoint'
import { listEvents } from '../services/api/live'
import { createWsTicket } from '../services/api/ws-ticket'
import type { WireEvent } from '../services/api/types'
import { classifyApiError } from '../services/api/client'
import { emptyBuffer, mergeEvent, type EventBuffer } from './events-core'

let socket: WebSocket | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
let pollTimer: ReturnType<typeof setInterval> | null = null
let backoffMs = 1000
let manuallyClosed = false
// 每次连接尝试的代际号。await 期间可能被新的 connect/disconnect 顶掉，
// 回调只对自己那一代生效，避免旧连接把新引用清空或再开第三条连接。
let generation = 0

interface EventsState extends EventBuffer {
  mode: 'off' | 'connecting' | 'ws' | 'poll'
  connected: boolean
  sessionId: string
  lastEventAt: number
  resyncs: number
  error: string
  // 按序待消费队列：accept 只入队，StagePage drain 后逐条派发给 live/transcript，
  // 避免批量回放（resync/poll）时只有最后一条生效。
  pending: WireEvent[]
}

export const useEventsStore = defineStore('events', {
  state: (): EventsState => ({
    ...emptyBuffer(),
    mode: 'off',
    connected: false,
    sessionId: '',
    lastEventAt: 0,
    resyncs: 0,
    error: '',
    pending: []
  }),
  getters: {
    fresh: (s) => (s.lastEventAt ? Math.max(0, Date.now() - s.lastEventAt) : -1),
    degraded: (s) => s.mode === 'poll'
  },
  actions: {
    // 只在真正换场次时清空事件列表并重置去重表/seq。
    // 空 sessionId（未开播/已停止）不清空，保留上一场可见列表。
    switchSession(sessionId: string): boolean {
      if (!sessionId || sessionId === this.sessionId) {
        this.sessionId = sessionId || this.sessionId
        return false
      }
      this.sessionId = sessionId
      this.events = []
      this.seen = {}
      this.lastSeq = 0
      this.epoch = 0
      this.dropped = 0
      this.droppedMalformed = 0
      this.pending = []
      this.lastEventAt = 0
      return true
    },

    async connect(sessionId = ''): Promise<void> {
      manuallyClosed = false
      const switched = this.switchSession(sessionId)
      // 换场次必须断开旧连接，否则会继续收到上一场事件。
      if (switched && socket) {
        generation += 1
        const old = socket
        socket = null
        old.close()
      }
      // 已有可用连接时直接复用；CLOSING 不算，允许换代。
      if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return
      const gen = ++generation
      this.mode = 'connecting'
      this.error = ''
      try {
        const ticket = await createWsTicket(sessionId)
        if (gen !== generation || manuallyClosed) return
        const ws = new WebSocket(wsEventsUrl(this.lastSeq, sessionId), ['dh.v1', `ticket.${ticket.ticket}`])
        if (gen !== generation || manuallyClosed) {
          ws.close()
          return
        }
        socket = ws
        ws.onopen = () => {
          if (gen !== generation) return
          backoffMs = 1000
          this.connected = true
          this.mode = 'ws'
          this.stopPolling()
          this.clearReconnect()
        }
        ws.onmessage = (ev) => {
          if (gen !== generation) return
          try {
            const data = JSON.parse(String(ev.data)) as WireEvent | { type: string; event?: WireEvent }
            if ('seq' in data) this.accept(data)
            else if ('event' in data && data.event) this.accept(data.event)
          } catch {
            this.error = '事件解析失败'
          }
        }
        ws.onclose = () => {
          // 旧代际的关闭事件不得影响当前连接。
          if (gen !== generation) return
          if (socket === ws) socket = null
          this.connected = false
          if (manuallyClosed) return
          this.mode = 'poll'
          this.startPolling()
          this.scheduleReconnect(sessionId)
        }
        ws.onerror = () => {
          if (gen !== generation) return
          this.error = 'WebSocket 连接失败'
        }
      } catch (err) {
        if (gen !== generation || manuallyClosed) return
        const c = classifyApiError(err)
        this.error = c.message
        this.mode = 'poll'
        this.startPolling()
        this.scheduleReconnect(sessionId)
      }
    },

    scheduleReconnect(sessionId: string): void {
      if (reconnectTimer || manuallyClosed) return
      const delay = backoffMs
      backoffMs = Math.min(backoffMs * 2, 30000)
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null
        if (document.visibilityState === 'hidden') return
        void this.connect(sessionId)
      }, delay)
    },

    clearReconnect(): void {
      if (reconnectTimer) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
    },

    accept(e: WireEvent): void {
      const outcome = mergeEvent(this.$state, e)
      this.droppedMalformed = outcome.buffer.droppedMalformed
      if (outcome.duplicate) return
      this.events = outcome.buffer.events
      this.seen = outcome.buffer.seen
      this.lastSeq = outcome.buffer.lastSeq
      this.epoch = outcome.buffer.epoch
      this.dropped = outcome.buffer.dropped
      this.pending.push(e)
      this.lastEventAt = e.ts_ms || Date.now()
      if (outcome.gap) void this.resync()
    },

    // 按到达顺序取走并清空待消费事件；消费方必须逐条应用。
    drain(): WireEvent[] {
      if (!this.pending.length) return []
      return this.pending.splice(0, this.pending.length)
    },

    async resync(): Promise<void> {
      this.resyncs += 1
      try {
        const events = await listEvents(this.lastSeq, this.sessionId)
        for (const e of events) this.accept(e)
      } catch (err) {
        this.error = classifyApiError(err).message
      }
    },

    async pollOnce(): Promise<void> {
      try {
        const events = await listEvents(this.lastSeq, this.sessionId)
        for (const e of events) this.accept(e)
      } catch (err) {
        this.error = classifyApiError(err).message
      }
    },

    startPolling(): void {
      if (pollTimer) return
      if (document.visibilityState === 'hidden') return
      pollTimer = setInterval(() => void this.pollOnce(), 15000)
    },

    stopPolling(): void {
      if (pollTimer) {
        clearInterval(pollTimer)
        pollTimer = null
      }
    },

    disconnect(): void {
      manuallyClosed = true
      generation += 1
      this.clearReconnect()
      this.stopPolling()
      const ws = socket
      socket = null
      ws?.close()
      this.connected = false
      this.mode = 'off'
    }
  }
})
