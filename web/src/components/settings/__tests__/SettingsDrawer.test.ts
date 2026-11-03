import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const getSettings = vi.fn()
vi.mock('../../../services/api/settings', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/settings')>('../../../services/api/settings')
  return {
    ...actual,
    getSettings: (...args: unknown[]) => getSettings(...args),
    saveSettings: vi.fn(),
    reloadSettings: vi.fn()
  }
})

import SettingsDrawer from '../SettingsDrawer.vue'
import { useSettingsStore } from '../../../stores/settings'

describe('SettingsDrawer', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSettings.mockReset()
    getSettings.mockResolvedValue({
      revision: 1,
      groups: [
        { id: 'vidu_key', title: 'Vidu Key', effective: 'next_session', values: { active_tail8: 'abcd1234efgh', backup_count: 2 } },
        { id: 'danmaku', title: '弹幕主播模式', effective: 'immediate', values: { mode: 'auto' } }
      ]
    })
  })

  it('渲染 12 个分组入口并默认展示会话组', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
    expect(w.findAll('.drawer__tab')).toHaveLength(12)
    expect(w.find('.drawer__group-head h3').text()).toBe('会话')
  })

  it('密钥只显示尾号，不渲染完整明文', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
    await w.findAll('.drawer__tab')[8].trigger('click') // vidu_key
    expect(w.find('.drawer__group-head h3').text()).toBe('Vidu Key')
    const secret = w.find('.nt-secret').text()
    expect(secret).toBe('***1234efgh')
    expect(w.html()).not.toContain('abcd1234efgh')
  })

  it('修改字段后出现未保存标记，保存按钮可用', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })

    const saveBtn = w.findAll('.foot__btn')[1]
    expect(saveBtn.attributes('disabled')).toBeDefined()

    store.setField('session', 'greeting', '你好')
    await w.vm.$nextTick()
    expect(w.find('.drawer__dirty').exists()).toBe(true)
    expect(w.findAll('.drawer__dot').length).toBeGreaterThan(0)
    expect(saveBtn.attributes('disabled')).toBeUndefined()
  })
})
