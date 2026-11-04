import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import NtDrawer from '../NtDrawer.vue'

describe('NtDrawer 响应式宽度', () => {
  it('inline 模式填满可用容器且仅以配置值限制最大宽度', () => {
    const w = mount(NtDrawer, {
      props: { open: true, title: '设置', width: 640, mode: 'inline' }
    })
    const el = w.get('.nt-drawer--inline').element as HTMLElement
    expect(el.style.width).toBe('100%')
    expect(el.style.maxWidth).toBe('640px')
    expect(w.attributes('role')).toBeUndefined()
  })

  it('modal 模式保留配置宽度并能通过 Escape 关闭', async () => {
    const w = mount(NtDrawer, {
      props: { open: true, title: '设置', width: 640 }
    })
    const el = w.get('.nt-drawer').element as HTMLElement
    expect(el.style.width).toBe('640px')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await w.vm.$nextTick()
    expect(w.emitted('close')).toEqual([[]])
  })
})
