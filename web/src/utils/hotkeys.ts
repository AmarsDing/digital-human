// 舞台快捷键统一登记：F9 开播、F10 停止、/ 聚焦输入框。Esc 由抽屉组件各自处理。
// Ctrl+K 命令面板不登记：v1 无命令注册表，避免暴露假功能。
export interface StageHotkeys {
  onStart?: () => void
  onStop?: () => void
  onFocusComposer?: () => void
}

function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  if (!el || !el.tagName) return false
  return el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable
}

export function registerStageHotkeys(handlers: StageHotkeys): () => void {
  function onKey(e: KeyboardEvent): void {
    if (e.key === 'F9') {
      e.preventDefault()
      handlers.onStart?.()
      return
    }
    if (e.key === 'F10') {
      e.preventDefault()
      handlers.onStop?.()
      return
    }
    if (e.key === '/' && !isTypingTarget(e.target)) {
      e.preventDefault()
      handlers.onFocusComposer?.()
    }
  }
  document.addEventListener('keydown', onKey)
  return () => document.removeEventListener('keydown', onKey)
}
