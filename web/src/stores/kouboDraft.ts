import { defineStore } from 'pinia'

// 口播草稿（v2 §2.2/§2.5）：源文本/字数上限/勾选分段为跨 Tab 草稿，组件卸载不丢。
// 服务端刷新不得覆盖 dirty 草稿；拆分提交成功后草稿与服务端对齐并清除 dirty。
interface KouboDraftState {
  text: string
  maxChars: number
  picked: string[]
  dirty: boolean
}

export const useKouboDraftStore = defineStore('kouboDraft', {
  state: (): KouboDraftState => ({
    text: '',
    maxChars: 120,
    picked: [],
    dirty: false
  }),
  actions: {
    setText(text: string): void {
      this.text = text
      this.dirty = true
    },
    setMaxChars(n: number): void {
      this.maxChars = n
      this.dirty = true
    },
    togglePick(id: string): void {
      const i = this.picked.indexOf(id)
      if (i >= 0) this.picked.splice(i, 1)
      else this.picked.push(id)
    },
    clearPicked(): void {
      this.picked = []
    },
    // 仅在草稿未脏时接受服务端文本；dirty 时保留用户编辑。
    syncFromServer(text: string): void {
      if (this.dirty) return
      this.text = text
    },
    markSubmitted(): void {
      this.dirty = false
    }
  }
})
