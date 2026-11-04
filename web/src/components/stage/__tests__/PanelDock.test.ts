import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PanelDock from '../PanelDock.vue'

describe('PanelDock 工作区 Tabs', () => {
  it('复盘不混入工作区，方向键只移动焦点，Enter 才选择', async () => {
    const w = mount(PanelDock, {
      attachTo: document.body,
      props: { active: 'voice', pendingCount: 3, rightOpen: false }
    })
    const tabs = w.findAll('[role="tab"]')
    expect(tabs).toHaveLength(6)
    expect(w.text()).not.toContain('复盘')
    expect(tabs[0].attributes('aria-controls')).toBe('workspace-panel-voice')
    expect(w.get('.dock__badge').attributes('aria-label')).toBe('待确认 3 条')

    await tabs[0].trigger('focus')
    await tabs[0].trigger('keydown', { key: 'ArrowRight' })
    expect(document.activeElement).toBe(tabs[1].element)
    expect(w.emitted('select')).toBeUndefined()
    await tabs[1].trigger('keydown', { key: 'Enter' })
    expect(w.emitted('select')).toEqual([['command']])
    w.unmount()
  })
})
