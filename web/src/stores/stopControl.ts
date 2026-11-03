import { defineStore } from 'pinia'
import { useLiveStore } from './live'
import { usePlatformStore } from './platform'

// 唯一下播/停推控制器（v2 §3.2）：
// Stage/VideoStage/ComposerBar/F10 统一走 requestStopLive；StreamControlCard 走 requestStopStream。
// 在途忽略后续触发；失败保留真实状态并可重试；成功后各自读取后端真实状态，禁止互相伪报。
export type StopSource = 'stage' | 'video-stage' | 'composer' | 'f10' | 'stream-card'

interface StopControlState {
  liveConfirmOpen: boolean
  streamConfirmOpen: boolean
  liveStopping: boolean
  streamStopping: boolean
  liveError: string
  streamError: string
  liveSource: StopSource | ''
  streamSource: StopSource | ''
}

export const useStopControlStore = defineStore('stopControl', {
  state: (): StopControlState => ({
    liveConfirmOpen: false,
    streamConfirmOpen: false,
    liveStopping: false,
    streamStopping: false,
    liveError: '',
    streamError: '',
    liveSource: '',
    streamSource: ''
  }),
  actions: {
    // 下播请求：检查当前状态与在途；已直播中才允许打开确认框。
    requestStopLive(source: StopSource): void {
      if (this.liveConfirmOpen || this.liveStopping) return
      const live = useLiveStore()
      if (live.busy || !live.isLive) return
      this.liveSource = source
      this.liveError = ''
      this.liveConfirmOpen = true
    },

    // 停推请求：与下播各自独立，状态不混报。
    requestStopStream(source: StopSource): void {
      if (this.streamConfirmOpen || this.streamStopping) return
      const platform = usePlatformStore()
      if (platform.controlBusy || !platform.streamStoppable) return
      this.streamSource = source
      this.streamError = ''
      this.streamConfirmOpen = true
    },

    async confirmStopLive(): Promise<void> {
      if (this.liveStopping || !this.liveConfirmOpen) return
      this.liveStopping = true
      this.liveError = ''
      const live = useLiveStore()
      const ok = await live.stop('manual')
      this.liveStopping = false
      if (ok) {
        // 成功后真实状态由 live store 的 load() 回读（stableAction finally）。
        this.liveConfirmOpen = false
        this.liveSource = ''
        return
      }
      // 失败保留当前场真实状态与确认框，可重试。
      this.liveError = live.error || '下播失败，请重试'
    },

    cancelStopLive(): void {
      if (this.liveStopping) return
      this.liveConfirmOpen = false
      this.liveError = ''
      this.liveSource = ''
    },

    async confirmStopStream(): Promise<void> {
      if (this.streamStopping || !this.streamConfirmOpen) return
      this.streamStopping = true
      this.streamError = ''
      const platform = usePlatformStore()
      const ok = await platform.stop()
      this.streamStopping = false
      if (ok) {
        // 成功后真实状态由 platform store 的 refreshStream() 回读。
        this.streamConfirmOpen = false
        this.streamSource = ''
        return
      }
      this.streamError = platform.controlError || '停止推流失败，请重试'
    },

    cancelStopStream(): void {
      if (this.streamStopping) return
      this.streamConfirmOpen = false
      this.streamError = ''
      this.streamSource = ''
    }
  }
})
