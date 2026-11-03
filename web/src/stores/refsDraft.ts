import { defineStore } from 'pinia'
import type { RefsImage } from '../services/api/panels'

// 参考图待下发草稿 + 已下发快照（v2 §2.2/§5.2）：
// pending 为跨 Tab 草稿（本地待提交内容，不得冒充已生效）；snapshots 存最近 3 组
// 「用户已提交内容 + HTTP 状态 + Vidu ack 状态」，HTTP 已受理与 ack 已应用严格分离。
export interface RefsSnapshot {
  images: RefsImage[]
  http: 'ok' | 'error'
  httpError: string
  ack: 'pending' | 'success' | 'failed'
  ackError: string
  atMs: number
}

const MAX_SNAPSHOTS = 3

interface RefsDraftState {
  pending: RefsImage[]
  dirty: boolean
  snapshots: RefsSnapshot[]
}

export const useRefsDraftStore = defineStore('refsDraft', {
  state: (): RefsDraftState => ({
    pending: [],
    dirty: false,
    snapshots: []
  }),
  getters: {
    // 无旧快照（上一组已提交内容）时不显示撤销。
    canUndo: (s) => s.snapshots.length >= 2
  },
  actions: {
    add(image: RefsImage): void {
      this.pending.push(image)
      this.dirty = true
    },
    removeAt(index: number): void {
      this.pending.splice(index, 1)
      this.dirty = true
    },
    // 服务端刷新不覆盖 dirty 草稿；仅在未脏时对齐服务端已下发内容。
    syncFromServer(images: RefsImage[]): void {
      if (this.dirty) return
      this.pending = [...images]
    },
    clearDirty(): void {
      this.dirty = false
    },
    pushSnapshot(snap: RefsSnapshot): void {
      this.snapshots.unshift(snap)
      if (this.snapshots.length > MAX_SNAPSHOTS) this.snapshots.length = MAX_SNAPSHOTS
    },
    // 恢复上一组/重试 ack 失败：就地更新该快照的回执状态，不伪造新提交。
    updateSnapshot(index: number, patch: Partial<Omit<RefsSnapshot, 'images' | 'atMs'>>): void {
      const snap = this.snapshots[index]
      if (!snap) return
      Object.assign(snap, patch)
    }
  }
})
