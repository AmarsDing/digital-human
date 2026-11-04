import { describe, expect, it } from 'vitest'
import { mount, VueWrapper } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import TaskControlDock from '../TaskControlDock.vue'

const DraftField = defineComponent({
  data: () => ({ draft: '保留我' }),
  template: '<input v-model="draft" aria-label="播出内容">'
})

const groups = [
  { id: 'live', label: '播出中', summary: '互动、动作与歌曲' },
  { id: 'prepare', label: '场次准备', summary: '形象、音色与口播' },
  { id: 'settings', label: '设置', summary: '平台与偏好' }
]

function mountDock() {
  let wrapper: VueWrapper
  wrapper = mount(TaskControlDock, {
    props: {
      groups,
      expandedId: 'live',
      'onUpdate:expandedId': (id: string) => wrapper.setProps({ expandedId: id }),
      quickActions: [
        { id: 'captions', label: '字幕' },
        { id: 'actions', label: '动作' },
        { id: 'songs', label: '歌曲' }
      ]
    },
    slots: {
      live: DraftField,
      prepare: '<div>准备内容</div>',
      settings: '<div>设置内容</div>'
    }
  })
  return wrapper
}

describe('TaskControlDock', () => {
  it('快捷操作常驻可见并向上发出动作', async () => {
    const w = mountDock()
    const action = w.findAll('button').find((button) => button.text() === '动作')
    expect(action?.isVisible()).toBe(true)
    await action?.trigger('click')
    expect(w.emitted('quick-action')).toEqual([['actions']])
  })

  it('三层手风琴同一时间只展开一个组', async () => {
    const w = mountDock()
    expect(w.get('[data-group-panel="live"]').attributes('style') ?? '').not.toContain('display: none')
    expect(w.get('[data-group-panel="prepare"]').attributes('style')).toContain('display: none')
    const prepare = w.get('button[aria-controls$="prepare"]')
    await prepare.trigger('click')
    await nextTick()
    expect(w.emitted('update:expandedId')?.at(-1)).toEqual(['prepare'])
    expect(w.get('[data-group-panel="live"]').attributes('style')).toContain('display: none')
    expect(w.get('[data-group-panel="prepare"]').attributes('style') ?? '').not.toContain('display: none')
  })

  it('折叠不卸载面板，保留直播中的输入草稿', async () => {
    const w = mountDock()
    const input = w.get('input[aria-label="播出内容"]')
    await input.setValue('未发送草稿')
    await w.get('button[aria-controls$="prepare"]').trigger('click')
    await nextTick()
    expect(w.get('[data-group-panel="live"]').attributes('style')).toContain('display: none')
    expect((w.get('input[aria-label="播出内容"]').element as HTMLInputElement).value).toBe('未发送草稿')
  })

  it('再次点击当前组可全部收起', async () => {
    const w = mountDock()
    await w.get('button[aria-controls$="live"]').trigger('click')
    await nextTick()
    expect(w.emitted('update:expandedId')?.at(-1)).toEqual([''])
    expect(w.get('[data-group-panel="live"]').attributes('style')).toContain('display: none')
  })
})
