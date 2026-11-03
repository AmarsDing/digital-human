import { readFileSync } from 'node:fs'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { AvatarListItem } from '../../services/api/panels'

const h = vi.hoisted(() => ({
  listAvatars: vi.fn(),
  activateAvatar: vi.fn(),
  deleteAvatar: vi.fn(),
  duplicateAvatar: vi.fn(),
  fetchAvatarImage: vi.fn(),
  push: vi.fn()
}))

vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return {
    ...actual,
    listAvatars: (...args: unknown[]) => h.listAvatars(...args),
    activateAvatar: (...args: unknown[]) => h.activateAvatar(...args),
    deleteAvatar: (...args: unknown[]) => h.deleteAvatar(...args),
    duplicateAvatar: (...args: unknown[]) => h.duplicateAvatar(...args),
    fetchAvatarImage: (...args: unknown[]) => h.fetchAvatarImage(...args)
  }
})

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRouter: () => ({ push: h.push }) }
})

import AvatarsPage from '../AvatarsPage.vue'

// R3-05：A6 列表页最小页面测试——挂载冒烟 + 关键交互（激活通路 / 空态跳转）。
// R3-01 回归：列表项带 avatar_asset_id 且同步 done 时「设为当前」可点击并调通 store。
function item(patch: Partial<AvatarListItem>): AvatarListItem {
  return {
    persona_id: 'p1',
    slug: 'xiao-ya',
    name: '小雅',
    vidu_sync_state: { state: 'done' },
    is_active: false,
    revision: 1,
    ...patch
  }
}

describe('AvatarsPage（R3-05）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    h.listAvatars.mockResolvedValue({ avatars: [], total: 0, storage: 'sqlite' })
    h.fetchAvatarImage.mockRejectedValue(new Error('no image'))
    h.activateAvatar.mockResolvedValue({})
    h.duplicateAvatar.mockResolvedValue(item({}))
  })

  it('空态：挂载冒烟 + 点击创建跳新建页', async () => {
    const w = mount(AvatarsPage)
    await flushPromises()
    expect(w.text()).toContain('数字人档案')
    expect(w.text()).toContain('暂无数字人档案')
    const create = w.findAll('button').find((b) => b.text() === '创建第一个档案')
    await create!.trigger('click')
    expect(h.push).toHaveBeenCalledWith('/avatars/new')
  })

  it('mem 存储提示条', async () => {
    h.listAvatars.mockResolvedValue({ avatars: [], total: 0, storage: 'memory' })
    const w = mount(AvatarsPage)
    await flushPromises()
    expect(w.find('[role="status"]').text()).toContain('内存存储')
  })

  it('激活通路：有图+done 的卡片点击「设为当前」调 activateAvatar 并刷新列表', async () => {
    h.listAvatars.mockResolvedValue({ avatars: [item({ avatar_asset_id: 'a1' })], total: 1, storage: 'sqlite' })
    const w = mount(AvatarsPage)
    await flushPromises()
    const btn = w.findAll('button').find((b) => b.text() === '设为当前')
    expect(btn!.attributes('disabled')).toBeUndefined()
    await btn!.trigger('click')
    await flushPromises()
    expect(h.activateAvatar).toHaveBeenCalledWith('p1')
    // activate 后 store 重新 load 列表
    expect(h.listAvatars.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('焦点环存在性（样式静态断言）', () => {
    const raw = readFileSync('src/pages/AvatarsPage.vue', 'utf-8')
    expect(raw).toContain(':focus-visible')
    expect(raw).toContain('outline-offset')
  })
})
