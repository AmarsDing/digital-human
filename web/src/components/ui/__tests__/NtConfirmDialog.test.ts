import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import NtConfirmDialog from '../NtConfirmDialog.vue'

function mountDialog(props: Record<string, unknown>, attach = true) {
  return mount(NtConfirmDialog, {
    props: { open: true, title: '下播确认', message: '确定下播？', ...props },
    attachTo: attach ? document.body : undefined
  })
}

function keydown(key: string, init: KeyboardEventInit = {}): void {
  document.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))
}

describe('NtConfirmDialog dangerous 合同（§3.1/§3.4）', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('打开后默认焦点落在取消按钮，不在危险确认按钮', async () => {
    const w = mountDialog({ dangerous: true, confirmText: '确认下播' })
    await nextTick()
    const btns = w.findAll('button')
    expect(document.activeElement).toBe(btns[0].element)
    expect(btns[0].text()).toContain('取消')
    w.unmount()
  })

  it('焦点在取消时按 Enter：只取消并回焦，绝不 confirm', async () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()
    const w = mountDialog({ dangerous: true })
    await nextTick()
    // 全局 Enter 不触发任何业务操作
    keydown('Enter')
    expect(w.emitted('confirm')).toBeUndefined()
    expect(w.emitted('cancel')).toBeUndefined()
    // 原生激活取消按钮（浏览器对聚焦 button 的 Enter 激活）
    w.findAll('button')[0].element.click()
    expect(w.emitted('confirm')).toBeUndefined()
    expect(w.emitted('cancel')).toHaveLength(1)
    // 关闭后回焦触发元素
    await w.setProps({ open: false })
    expect(document.activeElement).toBe(trigger)
    w.unmount()
  })

  it('非按钮区域按 Enter 无任何业务请求', async () => {
    const w = mountDialog({ dangerous: true })
    await nextTick()
    ;(document.activeElement as HTMLElement)?.blur?.()
    keydown('Enter')
    keydown(' ')
    expect(w.emitted('confirm')).toBeUndefined()
    expect(w.emitted('cancel')).toBeUndefined()
    w.unmount()
  })

  it('危险确认按钮原生激活只 confirm 一次语义；busy 期间按键无效', async () => {
    const w = mountDialog({ dangerous: true })
    await nextTick()
    const confirmBtn = w.findAll('button')[1]
    confirmBtn.element.focus()
    confirmBtn.element.click() // Enter/Space 的原生激活
    expect(w.emitted('confirm')).toHaveLength(1)
    // busy 期间：Enter 无效、点击无效、Esc 无效
    await w.setProps({ busy: true })
    keydown('Enter')
    keydown('Escape')
    confirmBtn.element.click()
    expect(w.emitted('confirm')).toHaveLength(1)
    expect(w.emitted('cancel')).toBeUndefined()
    expect(confirmBtn.attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('Esc 否定式关闭：emit cancel 并回焦，不触发 confirm', async () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()
    const w = mountDialog({ dangerous: true })
    await nextTick()
    keydown('Escape')
    expect(w.emitted('confirm')).toBeUndefined()
    expect(w.emitted('cancel')).toHaveLength(1)
    await w.setProps({ open: false })
    expect(document.activeElement).toBe(trigger)
    w.unmount()
  })

  it('失败错误以 role="alert" 呈现', async () => {
    const w = mountDialog({ dangerous: true, error: '下播失败，请重试' })
    await nextTick()
    const alert = w.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('下播失败')
    w.unmount()
  })
})

describe('NtConfirmDialog 非危险确认回归（§3.4 用例 11）', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('初始焦点在确认按钮，任意 Enter 确认，Esc 取消', async () => {
    const w = mountDialog({ confirmText: '清空' })
    await nextTick()
    const btns = w.findAll('button')
    expect(document.activeElement).toBe(btns[1].element)
    keydown('Enter')
    expect(w.emitted('confirm')).toHaveLength(1)
    keydown('Escape')
    expect(w.emitted('cancel')).toHaveLength(1)
    w.unmount()
  })

  it('Tab 焦点捕获仍在两按钮间循环', async () => {
    const w = mountDialog({})
    await nextTick()
    const btns = w.findAll('button')
    btns[1].element.focus()
    keydown('Tab')
    expect(document.activeElement).toBe(btns[0].element)
    keydown('Tab', { shiftKey: true })
    expect(document.activeElement).toBe(btns[1].element)
    w.unmount()
  })
})
