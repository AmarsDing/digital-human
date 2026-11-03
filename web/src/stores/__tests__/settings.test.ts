import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { GROUP_SPECS } from '../../components/settings/schema'

const getSettings = vi.fn()
const saveSettings = vi.fn()
const reloadSettings = vi.fn()

vi.mock('../../services/api/settings', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/settings')>('../../services/api/settings')
  return {
    ...actual,
    getSettings: (...args: unknown[]) => getSettings(...args),
    saveSettings: (...args: unknown[]) => saveSettings(...args),
    reloadSettings: (...args: unknown[]) => reloadSettings(...args)
  }
})

import { useSettingsStore } from '../settings'

const BACKEND_GROUPS = [
  { id: 'session', title: '会话', effective: 'next_session', values: { avatar_id: 'a-1' } },
  { id: 'danmaku', title: '弹幕主播模式', effective: 'immediate', values: { mode: 'confirm' } }
]

describe('settings store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSettings.mockReset()
    saveSettings.mockReset()
    reloadSettings.mockReset()
  })

  it('合并 schema 默认值，并补齐后端缺失分组', async () => {
    getSettings.mockResolvedValue({ groups: BACKEND_GROUPS, revision: 3 })
    const store = useSettingsStore()
    await store.load()

    expect(store.groups).toHaveLength(13)
    expect(store.groups.find((g) => g.id === 'session')?.values.avatar_id).toBe('a-1')
    expect(store.groups.find((g) => g.id === 'session')?.values.model).toBe('vidu-s2')
    expect(store.groups.find((g) => g.id === 'voice')?.values.frame_ms).toBe('100')
    expect(store.missingGroups).toContain('voice')
    expect(store.revision).toBe(3)
    expect(store.dirtyCount).toBe(0)
  })

  it('setField 只标记该组脏，保存后基线重置', async () => {
    getSettings.mockResolvedValue({ groups: BACKEND_GROUPS, revision: 3 })
    saveSettings.mockResolvedValue({ saved: true, revision: 4, reloadRequired: [], rejected: [] })
    const store = useSettingsStore()
    await store.load()

    store.setField('voice', 'voice_id', 'v-1')
    expect(store.dirtyGroupIds).toEqual(['voice'])

    const saved = await store.save()
    expect(saved).toBe(true)
    expect(saveSettings).toHaveBeenCalledTimes(1)
    const [groups, revision] = saveSettings.mock.calls[0]
    expect(revision).toBe(3)
    expect(groups).toHaveLength(13)
    expect(store.revision).toBe(4)
    expect(store.dirtyCount).toBe(0)
  })

  it('保存被拒绝时保留未保存状态并给出原因', async () => {
    getSettings.mockResolvedValue({ groups: BACKEND_GROUPS, revision: 3 })
    saveSettings.mockResolvedValue({ saved: false, revision: 3, reloadRequired: [], rejected: ['session'] })
    const store = useSettingsStore()
    await store.load()
    store.setField('session', 'avatar_id', 'bad')

    expect(await store.save()).toBe(false)
    expect(store.dirtyGroupIds).toEqual(['session'])
    expect(store.error).toContain('session')
  })

  it('加载失败暴露错误且不产生脏组', async () => {
    getSettings.mockRejectedValue(new TypeError('network down'))
    const store = useSettingsStore()
    await store.load()
    expect(store.error).toContain('无法连接')
    expect(store.dirtyCount).toBe(0)
  })

  it('schema 固定 13 组且顺序稳定（F 批新增 gift）', () => {
    expect(GROUP_SPECS.map((g) => g.id)).toEqual([
      'session',
      'voice',
      'member',
      'persona',
      'gift',
      'llm',
      'rtc',
      'action_library',
      'assets',
      'vidu_key',
      'captions',
      'danmaku',
      'misc'
    ])
  })
})
