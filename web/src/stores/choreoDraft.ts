import { defineStore } from 'pinia'

// 编排/动作编辑草稿（v2 §2.2）：建库/加动作表单等编辑内容跨 Tab 保留，组件卸载不丢。
interface ChoreoDraftState {
  libName: string
  actionName: string
  dirty: boolean
}

export const useChoreoDraftStore = defineStore('choreoDraft', {
  state: (): ChoreoDraftState => ({
    libName: '',
    actionName: '',
    dirty: false
  }),
  actions: {
    setLibName(v: string): void {
      this.libName = v
      this.dirty = true
    },
    setActionName(v: string): void {
      this.actionName = v
      this.dirty = true
    },
    clearLibName(): void {
      this.libName = ''
    },
    clearActionName(): void {
      this.actionName = ''
    }
  }
})
