import { defineStore } from 'pinia'
import { getDraft, type DraftDetail } from '../services/api/drafts'

const OWNER_KEY = 'dh.prepare.owner.v1'
const SELECTION_KEY = 'dh.prepare.selection.v1'

export const usePreparationStore = defineStore('preparation', {
  state: () => ({ owner: '', selected: null as DraftDetail | null, loaded: false }),
  actions: {
    async load(): Promise<void> {
      if (!this.owner) {
        try { this.owner = localStorage.getItem(OWNER_KEY) || '' } catch { /* 存储不可用仍允许本页操作 */ }
        if (!this.owner) {
          this.owner = `operator-${crypto.randomUUID()}`
          try { localStorage.setItem(OWNER_KEY, this.owner) } catch { /* 不持久化令牌或草稿正文 */ }
        }
      }
      if (this.loaded) return
      let id = ''
      try { id = localStorage.getItem(SELECTION_KEY) || '' } catch { /* 无持久选择 */ }
      // 仅保存 ID；版本、提交状态和资产引用每次启动都回源读取。
      if (id) {
        try { this.selected = await getDraft(id, this.owner) }
        catch { this.select(null) }
      }
      this.loaded = true
    },
    select(draft: DraftDetail | null): void {
      this.selected = draft?.committed ? draft : null
      try {
        if (this.selected) localStorage.setItem(SELECTION_KEY, this.selected.draft_id)
        else localStorage.removeItem(SELECTION_KEY)
      } catch { /* 当前内存选择仍有效 */ }
    }
  }
})
