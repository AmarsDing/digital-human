import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import RightRail from '../RightRail.vue'

describe('RightRail Tabs', () => {
  it('关联 tab/tabpanel，方向键移动焦点，Enter 才切换', async () => {
    const w = mount(RightRail, {
      attachTo: document.body,
      props: { lines: [], events: [], freshMs: -1, protocol: 'poll', showEvents: true }
    })
    const tabs = w.findAll('[role="tab"]')
    expect(tabs).toHaveLength(2)
    expect(tabs[0].attributes('aria-controls')).toBe('rail-panel-transcript')
    expect(w.find('#rail-panel-transcript').attributes('aria-labelledby')).toBe('rail-tab-transcript')

    await tabs[0].trigger('focus')
    await tabs[0].trigger('keydown', { key: 'ArrowRight' })
    expect(document.activeElement).toBe(tabs[1].element)
    expect(w.find('#rail-panel-transcript').exists()).toBe(true)
    await tabs[1].trigger('keydown', { key: 'Enter' })
    expect(w.find('#rail-panel-events').exists()).toBe(true)
    expect(tabs[1].attributes('aria-selected')).toBe('true')
    w.unmount()
  })
})
