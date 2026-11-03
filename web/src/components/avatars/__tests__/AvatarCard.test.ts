import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AvatarListItem } from '../../../services/api/panels'

vi.mock('../../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/panels')>('../../../services/api/panels')
  return {
    ...actual,
    fetchAvatarImage: vi.fn().mockRejectedValue(new Error('no image'))
  }
})

import AvatarCard from '../AvatarCard.vue'

// R3-01 回归：列表页「设为当前」预检三态——无图禁用 / 未同步禁用 / 有图+done 可激活。
// 预检依赖列表项 avatar_asset_id（后端补齐下发，与 detail 同键名）。
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

describe('AvatarCard 置 active 预检（R3-01）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  function activateBtn(w: ReturnType<typeof mount>) {
    return w.findAll('button').find((b) => b.text() === '设为当前')
  }

  it('无形象图（avatar_asset_id 缺失）：禁用并提示先上传', async () => {
    const w = mount(AvatarCard, { props: { avatar: item({}) } })
    await flushPromises()
    const btn = activateBtn(w)
    expect(btn).toBeTruthy()
    expect(btn!.attributes('disabled')).toBeDefined()
    expect(btn!.attributes('title')).toContain('请先上传形象图')
  })

  it('有图但未同步 Vidu（state≠done）：禁用并提示未同步', async () => {
    const w = mount(AvatarCard, { props: { avatar: item({ avatar_asset_id: 'a1', vidu_sync_state: { state: 'queued' } }) } })
    await flushPromises()
    const btn = activateBtn(w)
    expect(btn!.attributes('disabled')).toBeDefined()
    expect(btn!.attributes('title')).toContain('未同步 Vidu')
  })

  it('有图且同步完成：可激活，点击 emit activate', async () => {
    const w = mount(AvatarCard, { props: { avatar: item({ avatar_asset_id: 'a1' }) } })
    await flushPromises()
    const btn = activateBtn(w)
    expect(btn!.attributes('disabled')).toBeUndefined()
    await btn!.trigger('click')
    expect(w.emitted('activate')).toEqual([['p1']])
  })

  it('当前档案：禁用（不重复置 active）', async () => {
    const w = mount(AvatarCard, { props: { avatar: item({ avatar_asset_id: 'a1', is_active: true }) } })
    await flushPromises()
    const btn = activateBtn(w)
    expect(btn!.attributes('disabled')).toBeDefined()
    expect(w.find('.card__badge').text()).toBe('当前')
  })
})
