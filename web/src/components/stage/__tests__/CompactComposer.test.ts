import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import CompactComposer from '../CompactComposer.vue'

function mountComposer(props: { canSpeak?: boolean; busy?: boolean; canInterrupt?: boolean } = {}) {
  return mount(CompactComposer, { props })
}

describe('CompactComposer', () => {
  it('语音主入口常驻可见，未接入时明确禁用', () => {
    const w = mountComposer()
    const voice = w.get('button[aria-label="语音交互待接入"]')
    expect(voice.isVisible()).toBe(true)
    expect(voice.attributes('disabled')).toBeDefined()
    expect(voice.text()).toContain('语音')
  })

  it('Enter 发送去空白文本并清空输入', async () => {
    const w = mountComposer()
    const input = w.get('textarea')
    await input.setValue('  大家好  ')
    await input.trigger('keydown', { key: 'Enter', shiftKey: false })
    expect(w.emitted('send')).toEqual([['大家好']])
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([''])
    expect((input.element as HTMLTextAreaElement).value).toBe('')
  })

  it('Shift+Enter 和空白内容不会发送', async () => {
    const w = mountComposer()
    const input = w.get('textarea')
    await input.setValue('第一行')
    await input.trigger('keydown', { key: 'Enter', shiftKey: true })
    await input.setValue('   ')
    await input.trigger('keydown', { key: 'Enter', shiftKey: false })
    expect(w.emitted('send')).toBeUndefined()
  })

  it('未开播或忙碌时不能发送', async () => {
    const w = mountComposer({ canSpeak: false })
    expect(w.get('textarea').attributes('disabled')).toBeDefined()
    await w.setProps({ canSpeak: true, busy: true })
    await w.get('textarea').setValue('等待中')
    await w.get('textarea').trigger('keydown', { key: 'Enter', shiftKey: false })
    expect(w.emitted('send')).toBeUndefined()
  })

  it('打断、长文和更多设置都有显式事件入口', async () => {
    const w = mountComposer({ canInterrupt: true })
    await w.get('button[aria-label="打断当前播报"]').trigger('click')
    await w.get('button[aria-label="展开长文输入"]').trigger('click')
    await w.get('button[aria-label="更多输入设置"]').trigger('click')
    expect(w.emitted('interrupt')).toHaveLength(1)
    expect(w.emitted('expand')).toHaveLength(1)
    expect(w.emitted('more')).toHaveLength(1)
  })
})
