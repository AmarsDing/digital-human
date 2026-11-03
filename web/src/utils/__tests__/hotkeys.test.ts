import { afterEach, describe, expect, it, vi } from 'vitest'
import { registerStageHotkeys } from '../hotkeys'

function fire(key: string, init: KeyboardEventInit & { keyCode?: number } = {}, target?: EventTarget): KeyboardEvent {
  const { keyCode, ...rest } = init
  const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...rest })
  if (keyCode !== undefined) Object.defineProperty(e, 'keyCode', { value: keyCode })
  ;(target ?? document).dispatchEvent(e)
  return e
}

describe('registerStageHotkeys F9/F10 上下文守卫（§3.3/§3.4）', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('长按 F10（event.repeat）只触发一次，只开一个确认框语义', () => {
    const onStop = vi.fn()
    const off = registerStageHotkeys({ onStop })
    fire('F10', { repeat: true })
    fire('F10', { repeat: true })
    expect(onStop).not.toHaveBeenCalled()
    fire('F10')
    expect(onStop).toHaveBeenCalledTimes(1)
    off()
  })

  it('中文 IME 组字（isComposing / keyCode 229）时 F9/F10 无副作用', () => {
    const onStart = vi.fn()
    const onStop = vi.fn()
    const off = registerStageHotkeys({ onStart, onStop })
    fire('F9', { isComposing: true })
    fire('F10', { keyCode: 229 })
    expect(onStart).not.toHaveBeenCalled()
    expect(onStop).not.toHaveBeenCalled()
    off()
  })

  it('焦点在输入框/textarea/contenteditable 时 F9/F10 无副作用', () => {
    const onStart = vi.fn()
    const onStop = vi.fn()
    const off = registerStageHotkeys({ onStart, onStop })
    const input = document.createElement('input')
    document.body.appendChild(input)
    fire('F9', {}, input)
    fire('F10', {}, input)
    const ta = document.createElement('textarea')
    document.body.appendChild(ta)
    fire('F10', {}, ta)
    expect(onStart).not.toHaveBeenCalled()
    expect(onStop).not.toHaveBeenCalled()
    off()
  })

  it('抽屉/菜单/确认框等顶层弹层打开时 F9/F10 无副作用；v-show 关闭的弹层不拦截', () => {
    const onStart = vi.fn()
    const onStop = vi.fn()
    const off = registerStageHotkeys({ onStart, onStop })
    const drawer = document.createElement('div')
    drawer.setAttribute('role', 'dialog')
    drawer.setAttribute('aria-modal', 'true')
    drawer.style.display = 'none' // v-show 关闭态
    document.body.appendChild(drawer)
    fire('F9')
    expect(onStart).toHaveBeenCalledTimes(1)
    drawer.style.display = '' // 抽屉打开
    fire('F9')
    fire('F10')
    expect(onStart).toHaveBeenCalledTimes(1)
    expect(onStop).not.toHaveBeenCalled()
    drawer.remove()
    const confirm = document.createElement('div')
    confirm.className = 'nt-confirm__mask'
    document.body.appendChild(confirm)
    fire('F9')
    fire('F10')
    expect(onStart).toHaveBeenCalledTimes(1)
    expect(onStop).not.toHaveBeenCalled()
    off()
  })

  it('isBlocked（live/stream busy）拦截 F9/F10；canStart=false 只拦截 F9', () => {
    const onStart = vi.fn()
    const onStop = vi.fn()
    let blocked = false
    let canStart = true
    const off = registerStageHotkeys({
      onStart,
      onStop,
      isBlocked: () => blocked,
      canStart: () => canStart
    })
    blocked = true
    fire('F9')
    fire('F10')
    expect(onStart).not.toHaveBeenCalled()
    expect(onStop).not.toHaveBeenCalled()
    blocked = false
    canStart = false
    fire('F9')
    expect(onStart).not.toHaveBeenCalled()
    // F10 不受 canStart 影响，可打开下播确认
    fire('F10')
    expect(onStop).toHaveBeenCalledTimes(1)
    off()
  })

  it('正常状态 F9 触发 onStart、F10 只请求确认（onStop）', () => {
    const onStart = vi.fn()
    const onStop = vi.fn()
    const off = registerStageHotkeys({ onStart, onStop })
    const e9 = fire('F9')
    const e10 = fire('F10')
    expect(onStart).toHaveBeenCalledTimes(1)
    expect(onStop).toHaveBeenCalledTimes(1)
    expect(e9.defaultPrevented).toBe(true)
    expect(e10.defaultPrevented).toBe(true)
    off()
  })
})
