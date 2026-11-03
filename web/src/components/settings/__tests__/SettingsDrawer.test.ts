import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
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

const listAvatars = vi.fn()
const getAvatar = vi.fn()
vi.mock('../../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/panels')>('../../../services/api/panels')
  return {
    ...actual,
    listAvatars: (...args: unknown[]) => listAvatars(...args),
    getAvatar: (...args: unknown[]) => getAvatar(...args)
  }
})

import SettingsDrawer from '../SettingsDrawer.vue'
import { useSettingsStore } from '../../../stores/settings'

describe('SettingsDrawer', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSettings.mockReset()
    listAvatars.mockReset()
    getAvatar.mockReset()
    // 默认无档案：只读派生键显示「未配置档案」
    listAvatars.mockResolvedValue({ avatars: [], total: 0, storage: 'memory' })
    getSettings.mockResolvedValue({
      revision: 1,
      groups: [
        { id: 'vidu_key', title: 'Vidu Key', effective: 'next_session', values: { active_tail8: 'abcd1234efgh', backup_count: 2 } },
        { id: 'danmaku', title: '弹幕主播模式', effective: 'immediate', values: { mode: 'auto' } }
      ]
    })
  })

  it('渲染 13 个分组入口并默认展示会话组', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
    expect(w.findAll('.drawer__tab')).toHaveLength(13)
    expect(w.find('.drawer__group-head h3').text()).toBe('会话')
  })

  it('人设分组徽标显示下次连线', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
    await w.findAll('.drawer__tab')[3].trigger('click')

    const effect = w.find('.drawer__effect')
    expect(effect.text()).toBe('下次连线')
    expect(effect.classes()).not.toContain('drawer__effect--immediate')
  })

  it('密钥只显示尾号，不渲染完整明文', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useSettingsStore()
    await store.load()
    const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
    await w.findAll('.drawer__tab')[9].trigger('click') // vidu_key（F 批 gift 组插在 persona 后）
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

  // R3-04：7 个只读键展示 active 档案派生值（v3 §1.2），而非 settings 库存储旧值。
  describe('只读派生值（R3-04）', () => {
    function mockActiveAvatar(prompt: string): void {
      const item = {
        persona_id: 'p1',
        slug: 'xiao-ya',
        name: '小雅',
        vidu_avatar_id: 'vidu-1',
        vidu_image_uri: 'vidu://img',
        vidu_sync_state: { state: 'done' },
        is_active: true,
        revision: 1
      }
      listAvatars.mockResolvedValue({ avatars: [item], total: 1, storage: 'sqlite' })
      getAvatar.mockResolvedValue({
        ...item,
        system_prompt: prompt,
        greeting: '',
        voice_id: 'voice-zh',
        refs: []
      })
      getSettings.mockResolvedValue({
        revision: 1,
        groups: [
          { id: 'session', title: '会话', effective: 'next_session', values: { avatar_id: 'old-avatar', image_uri: 'old-uri' } },
          { id: 'persona', title: '人设', effective: 'next_session', values: { active_id: 'OLD_PERSONA', content: '旧存储值' } },
          { id: 'llm', title: 'LLM', effective: 'next_session', values: { system: '旧 system' } }
        ]
      })
    }

    async function mountOpen() {
      const pinia = createPinia()
      setActivePinia(pinia)
      const store = useSettingsStore()
      await store.load()
      const w = mount(SettingsDrawer, { props: { open: true }, global: { plugins: [pinia] } })
      await flushPromises()
      return w
    }

    it('persona/llm/session 只读键显示派生值（system_prompt 截 200 字预览）', async () => {
      mockActiveAvatar('设'.repeat(250))
      const w = await mountOpen()
      const preview = '设'.repeat(200) + '…'

      // persona 组（tab index 3）
      await w.findAll('.drawer__tab')[3].trigger('click')
      const ro = w.findAll('.field__readonly-value')
      expect(ro[0].text()).toBe('小雅（xiao-ya）')
      expect(ro[1].text()).toBe(preview)
      expect(w.text()).not.toContain('旧存储值')
      expect(w.text()).not.toContain('OLD_PERSONA')

      // llm 组 system 同预览（tab index 5，F 批 gift 组插在 persona 后）
      await w.findAll('.drawer__tab')[5].trigger('click')
      expect(w.findAll('.field__readonly-value')[0].text()).toBe(preview)

      // session 组 avatar_id / image_uri 显示档案 vidu 值（tab index 0）
      await w.findAll('.drawer__tab')[0].trigger('click')
      const texts = w.findAll('.field__readonly-value').map((x) => x.text())
      expect(texts).toContain('vidu-1')
      expect(texts).toContain('vidu://img')
      expect(texts).not.toContain('old-avatar')
    })

    it('voice 组 voice_id 显示档案音色', async () => {
      mockActiveAvatar('提示词')
      const w = await mountOpen()
      await w.findAll('.drawer__tab')[1].trigger('click')
      expect(w.findAll('.field__readonly-value')[0].text()).toBe('voice-zh')
    })

    it('无 active 档案时显示「未配置档案」', async () => {
      const w = await mountOpen()
      await w.findAll('.drawer__tab')[3].trigger('click')
      const ro = w.findAll('.field__readonly-value')
      expect(ro[0].text()).toBe('未配置档案')
      expect(ro[1].text()).toBe('未配置档案')
    })
  })
})
