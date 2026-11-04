import { afterEach, describe, expect, it } from 'vitest'
import { mount, VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import MetricsPopover from '../MetricsPopover.vue'

const items = [
  { id: 'points', label: '积分', value: '120' },
  { id: 'balance', label: '余额', value: '¥ 32.00' },
  { id: 'duration', label: '时长', value: '00:12:30' },
  { id: 'next', label: '下次开播', value: '20:00' },
  { id: 'cost', label: '费用', value: '¥ 1.20' },
  { id: 'billing', label: '计费', value: '按自然日' }
]

let wrapper: VueWrapper | undefined

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  document.body.innerHTML = ''
})

function mountPopover() {
  wrapper = mount(MetricsPopover, { props: { items }, attachTo: document.body })
  return wrapper
}

describe('MetricsPopover', () => {
  it('默认收起，点击后一次展示六项统计', async () => {
    const w = mountPopover()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    const trigger = w.get('button[aria-haspopup="dialog"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(w.get('[role="dialog"]').findAll('dt')).toHaveLength(6)
    expect(w.get('[role="dialog"]').text()).toContain('按自然日')
  })

  it('Alt+M 可反复呼出和收起', async () => {
    const w = mountPopover()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'm', altKey: true }))
    await nextTick()
    expect(w.find('[role="dialog"]').exists()).toBe(true)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'M', altKey: true }))
    await nextTick()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
  })

  it('Esc 关闭并把焦点还给触发按钮', async () => {
    const w = mountPopover()
    const trigger = w.get('button[aria-haspopup="dialog"]')
    ;(trigger.element as HTMLButtonElement).focus()
    await trigger.trigger('click')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
  })

  it('点击浮层外部关闭，点击内容不会误关闭', async () => {
    const w = mountPopover()
    const trigger = w.get('button[aria-haspopup="dialog"]')
    await trigger.trigger('click')
    w.get('[role="dialog"]').element.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await nextTick()
    expect(w.find('[role="dialog"]').exists()).toBe(true)
    document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await nextTick()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
  })
})
