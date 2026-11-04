import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import NtButton from '../NtButton.vue'

describe('NtButton', () => {
  it('loading 禁用点击并播报忙碌，type 透传给原生按钮', async () => {
    const w = mount(NtButton, {
      props: { loading: true, type: 'submit' },
      slots: { default: '保存' }
    })
    const button = w.get('button')
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('aria-busy')).toBe('true')
    expect(button.attributes('type')).toBe('submit')
    await button.trigger('click')
    expect(w.emitted('click')).toBeUndefined()

    await w.setProps({ loading: false })
    expect(button.attributes('disabled')).toBeUndefined()
    expect(button.attributes('aria-busy')).toBeUndefined()
    await button.trigger('click')
    expect(w.emitted('click')).toHaveLength(1)
  })

  it('默认使用 button，避免意外提交表单', () => {
    const w = mount(NtButton)
    expect(w.get('button').attributes('type')).toBe('button')
  })
})
