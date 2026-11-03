import { defineStore } from 'pinia'
import { classifyApiError } from '../services/api/client'
import {
  getCollectorStatus,
  getStreamStatus,
  saveCollectorConfig,
  saveStreamTarget,
  startStream,
  stopStream,
  type CollectorStatus,
  type StreamStatus
} from '../services/api/platform'

// 慢响应不覆盖新状态（与 live store 同款版本闸门）。
let streamVersion = 0
let collectorVersion = 0

interface PlatformState {
  stream: StreamStatus | null
  collector: CollectorStatus | null
  streamLoading: boolean
  collectorLoading: boolean
  streamError: string
  collectorError: string
  targetBusy: boolean
  targetError: string
  controlBusy: boolean
  controlError: string
  roomBusy: boolean
  roomError: string
}

export const usePlatformStore = defineStore('platform', {
  state: (): PlatformState => ({
    stream: null,
    collector: null,
    streamLoading: false,
    collectorLoading: false,
    streamError: '',
    collectorError: '',
    targetBusy: false,
    targetError: '',
    controlBusy: false,
    controlError: '',
    roomBusy: false,
    roomError: ''
  }),
  getters: {
    streamState: (s): string => s.stream?.state ?? 'idle',
    collectorState: (s): string => s.collector?.state ?? 'off',
    // 仅 starting/pushing 禁止重复点开始；degraded 允许重试与停止。
    streamBusy: (s): boolean => s.stream?.state === 'starting' || s.stream?.state === 'pushing',
    streamStoppable: (s): boolean =>
      s.stream?.state === 'starting' || s.stream?.state === 'pushing' || s.stream?.state === 'degraded'
  },
  actions: {
    async refreshStream(signal?: AbortSignal): Promise<void> {
      const version = ++streamVersion
      if (!this.stream) this.streamLoading = true
      try {
        const status = await getStreamStatus(signal)
        if (version !== streamVersion) return
        this.stream = status
        this.streamError = ''
      } catch (err) {
        if (version !== streamVersion) return
        this.streamError = classifyApiError(err).message
      } finally {
        if (version === streamVersion) this.streamLoading = false
      }
    },

    async refreshCollector(signal?: AbortSignal): Promise<void> {
      const version = ++collectorVersion
      if (!this.collector) this.collectorLoading = true
      try {
        const status = await getCollectorStatus(signal)
        if (version !== collectorVersion) return
        this.collector = status
        this.collectorError = ''
      } catch (err) {
        if (version !== collectorVersion) return
        this.collectorError = classifyApiError(err).message
      } finally {
        if (version === collectorVersion) this.collectorLoading = false
      }
    },

    async saveTarget(rtmpUrl: string, streamKey: string): Promise<boolean> {
      if (this.targetBusy) return false
      this.targetBusy = true
      this.targetError = ''
      try {
        const r = await saveStreamTarget(rtmpUrl, streamKey)
        if (!r.ok) {
          this.targetError = '保存失败，请稍后重试'
          return false
        }
        await this.refreshStream()
        return true
      } catch (err) {
        this.targetError = classifyApiError(err).message
        return false
      } finally {
        this.targetBusy = false
      }
    },

    async start(): Promise<boolean> {
      if (this.controlBusy) return false
      this.controlBusy = true
      this.controlError = ''
      try {
        const r = await startStream()
        if (!r.ok) this.controlError = r.reason || '开始推流被拒绝'
        await this.refreshStream()
        return r.ok
      } catch (err) {
        this.controlError = classifyApiError(err).message
        return false
      } finally {
        this.controlBusy = false
      }
    },

    async stop(): Promise<boolean> {
      if (this.controlBusy) return false
      this.controlBusy = true
      this.controlError = ''
      try {
        const r = await stopStream()
        if (!r.ok) this.controlError = '停止推流失败，请稍后重试'
        await this.refreshStream()
        return r.ok
      } catch (err) {
        this.controlError = classifyApiError(err).message
        return false
      } finally {
        this.controlBusy = false
      }
    },

    async saveRoom(roomId: string): Promise<boolean> {
      if (this.roomBusy) return false
      this.roomBusy = true
      this.roomError = ''
      try {
        const r = await saveCollectorConfig(roomId)
        if (!r.ok) {
          this.roomError = '保存失败，请稍后重试'
          return false
        }
        await this.refreshCollector()
        return true
      } catch (err) {
        this.roomError = classifyApiError(err).message
        return false
      } finally {
        this.roomBusy = false
      }
    }
  }
})
