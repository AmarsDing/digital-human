// 舞台快捷键统一登记：F9 开播、F10 请求下播确认、/ 聚焦输入框。Esc 由抽屉组件各自处理。
// Ctrl+K 命令面板不登记：v1 无命令注册表，避免暴露假功能。
// F9/F10 上下文守卫（v2 §3.3）：输入框/IME/keyCode 229/event.repeat/顶层弹层/busy 全部拦截；
// F10 只打开下播确认，不直接停止；F9 的就绪判定经 canStart 接入点（服务端 preflight 未就绪前不伪造 ready）。
export interface StageHotkeys {
  onStart?: () => void
  onStop?: () => void
  onFocusComposer?: () => void
  // F9 就绪判定接入点：返回 false 时拦截（如 busy/phase 不就绪；服务端 preflight 凭证可在此并入）。
  canStart?: () => boolean
  // 附加上下文守卫：返回 true 时拦截 F9/F10（如 live/stream busy）。
  isBlocked?: () => boolean
}

function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  if (!el || !el.tagName) return false
  return el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable
}

// 抽屉/菜单/模态/确认框等顶层弹层正在打开时拦截。v-show 关闭的弹层按 display:none 排除。
function hasBlockingLayer(): boolean {
  if (typeof document === 'undefined') return false
  const nodes = document.querySelectorAll(
    '.nt-confirm__mask, .q-dialog, .q-menu, [role="alertdialog"], [role="dialog"][aria-modal="true"]'
  )
  for (const n of Array.from(nodes)) {
    if (typeof getComputedStyle === 'function' && getComputedStyle(n).display === 'none') continue
    return true
  }
  return false
}

// 统一上下文守卫：返回 true 表示放行。
function allowed(e: KeyboardEvent, extra?: () => boolean): boolean {
  if (isTypingTarget(e.target)) return false
  // IME 组合输入（中文组字）及 keyCode 229。
  if (e.isComposing || e.keyCode === 229) return false
  // 长按只触发一次。
  if (e.repeat) return false
  if (hasBlockingLayer()) return false
  if (extra && !extra()) return false
  return true
}

export function registerStageHotkeys(handlers: StageHotkeys): () => void {
  function onKey(e: KeyboardEvent): void {
    if (e.key === 'F9') {
      if (!allowed(e, () => (handlers.isBlocked?.() ? false : handlers.canStart?.() ?? true))) return
      e.preventDefault()
      handlers.onStart?.()
      return
    }
    if (e.key === 'F10') {
      if (!allowed(e, () => !handlers.isBlocked?.())) return
      e.preventDefault()
      // F10 只请求打开下播确认，不直接停止。
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
