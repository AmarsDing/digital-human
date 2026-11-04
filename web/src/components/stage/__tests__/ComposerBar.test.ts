import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ComposerBar from '../ComposerBar.vue'
import VoiceControl from '../VoiceControl.vue'

function mountBar(canSpeak: boolean, busy = false) {
  return mount(ComposerBar, {
    props: { canSpeak, busy, sessionId: 's-1', epoch: 7 },
    global: { stubs: { VoiceControl: true } }
  })
}

describe('ComposerBar', () => {
  it('语音主入口常驻可见并接收当前场次参数', () => {
    const w = mountBar(true)
    const voice = w.findComponent(VoiceControl)
    expect(voice.exists()).toBe(true)
    expect(voice.props()).toMatchObject({ sessionId: 's-1', epoch: 7, canSpeak: true })
  })

  it('未开播时禁用输入与发送', () => {
    const w = mountBar(false)
    expect(w.find('textarea').attributes('disabled')).toBeDefined()
    const send = w.findAll('button').find((b) => b.text() === '发送')
    expect(send?.attributes('disabled')).toBeDefined()
  })

  it('Enter 发送按句文本并清空输入', async () => {
    const w = mountBar(true)
    const ta = w.find('textarea')
    await ta.setValue('  大家好  ')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: false })
    const emitted = w.emitted('say')
    expect(emitted?.[0]).toEqual(['大家好', 'sentence'])
    expect((ta.element as HTMLTextAreaElement).value).toBe('')
  })

  it('Shift+Enter 不发送，保留换行', async () => {
    const w = mountBar(true)
    const ta = w.find('textarea')
    await ta.setValue('第一行')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: true })
    expect(w.emitted('say')).toBeUndefined()
    expect((ta.element as HTMLTextAreaElement).value).toBe('第一行')
  })

  it('整段模式随选择器切换', async () => {
    const w = mountBar(true)
    const ta = w.find('textarea')
    await ta.setValue('整段播报')
    // select[0]=通道（口播/对话），select[1]=模式（按句/整段）。
    await w.findAll('select')[1].setValue('whole')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: false })
    expect(w.emitted('say')?.[0]).toEqual(['整段播报', 'whole'])
  })

  it('忙碌时不能发送', async () => {
    const w = mountBar(true, true)
    await w.find('textarea').setValue('x')
    await w.find('textarea').trigger('keydown', { key: 'Enter', shiftKey: false })
    expect(w.emitted('say')).toBeUndefined()
  })
})
