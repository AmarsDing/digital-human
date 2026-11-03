import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { AvatarDetail } from '../../services/api/panels'

const h = vi.hoisted(() => ({
  routeId: 'p1',
  getAvatar: vi.fn(),
  listAvatars: vi.fn(),
  listActionLibraries: vi.fn(),
  fetchAvatarImage: vi.fn(),
  fetchAvatarRefImage: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  leaveGuard: null as null | ((...args: unknown[]) => unknown)
}))

vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return {
    ...actual,
    getAvatar: (...args: unknown[]) => h.getAvatar(...args),
    listAvatars: (...args: unknown[]) => h.listAvatars(...args),
    listActionLibraries: (...args: unknown[]) => h.listActionLibraries(...args),
    fetchAvatarImage: (...args: unknown[]) => h.fetchAvatarImage(...args),
    fetchAvatarRefImage: (...args: unknown[]) => h.fetchAvatarRefImage(...args)
  }
})

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => ({ params: { id: h.routeId } }),
    useRouter: () => ({ push: h.push, replace: h.replace, back: h.back }),
    onBeforeRouteLeave: (fn: (...args: unknown[]) => unknown) => {
      h.leaveGuard = fn
    }
  }
})

// live store 的 load 走 api/live → fetch：测试里统一 503，验证页面挂载不依赖后端。
function offlineFetch(): Promise<Response> {
  return Promise.resolve({ ok: false, status: 503, json: async () => ({}), text: async () => '{}' } as Response)
}

import AvatarEditPage from '../AvatarEditPage.vue'
import AvatarPromptFields from '../../components/avatars/AvatarPromptFields.vue'
import NtConfirmDialog from '../../components/ui/NtConfirmDialog.vue'

// R3-05：A7 编辑页最小页面测试——挂载冒烟 + dirty 拦截 + 置 active 三态。
// Tab 序走查按 v3 §10 降级为人工清单，此处只自动断言焦点环/溢出样式存在性。
function detail(patch: Partial<AvatarDetail>): AvatarDetail {
  return {
    persona_id: 'p1',
    slug: 'xiao-ya',
    name: '小雅',
    system_prompt: '',
    greeting: '',
    voice_id: '',
    refs: [],
    vidu_sync_state: { state: 'done' },
    is_active: false,
    revision: 1,
    ...patch
  }
}

describe('AvatarEditPage（R3-05）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    h.routeId = 'p1'
    h.leaveGuard = null
    h.listAvatars.mockResolvedValue({ avatars: [], total: 0, storage: 'sqlite' })
    h.listActionLibraries.mockResolvedValue([])
    h.fetchAvatarImage.mockRejectedValue(new Error('no image'))
    h.fetchAvatarRefImage.mockRejectedValue(new Error('no image'))
    vi.stubGlobal('fetch', vi.fn(offlineFetch))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('新建模式：挂载冒烟，离开后守卫初始放行', async () => {
    h.routeId = ''
    const w = mount(AvatarEditPage)
    await flushPromises()
    expect(w.find('h1').text()).toBe('新建档案')
    expect(w.text()).toContain('基本信息')
    expect(h.leaveGuard).toBeTruthy()
    expect(h.leaveGuard!()).toBe(true)
  })

  it('dirty 拦截：修改表单后路由守卫拦截，确认放弃后离开', async () => {
    h.routeId = ''
    const w = mount(AvatarEditPage)
    await flushPromises()
    w.findComponent(AvatarPromptFields).vm.$emit('update:modelValue', {
      slug: 'xiao-ya',
      name: '小雅',
      system_prompt: '',
      greeting: ''
    })
    await flushPromises()
    expect(h.leaveGuard!()).toBe(false)
    await w.vm.$nextTick()
    const dialog = w.findComponent(NtConfirmDialog)
    expect(dialog.props('open')).toBe(true)
    dialog.vm.$emit('confirm')
    await flushPromises()
    expect(h.back).toHaveBeenCalled()
  })

  it('编辑模式置 active 三态：无图禁用 / 未同步禁用 / 同步完成可用', async () => {
    // 无图
    h.getAvatar.mockResolvedValue(detail({ avatar_asset_id: undefined }))
    let w = mount(AvatarEditPage)
    await flushPromises()
    let btn = w.findAll('button').find((b) => b.text() === '设为当前')
    expect(btn!.attributes('disabled')).toBeDefined()
    expect(btn!.attributes('title')).toContain('请先上传形象图')
    w.unmount()

    // 有图未同步
    h.getAvatar.mockResolvedValue(detail({ avatar_asset_id: 'a1', vidu_sync_state: { state: 'queued' } }))
    w = mount(AvatarEditPage)
    await flushPromises()
    btn = w.findAll('button').find((b) => b.text() === '设为当前')
    expect(btn!.attributes('disabled')).toBeDefined()
    expect(btn!.attributes('title')).toContain('未同步 Vidu')
    w.unmount()

    // 有图且 done：可用
    h.getAvatar.mockResolvedValue(detail({ avatar_asset_id: 'a1' }))
    w = mount(AvatarEditPage)
    await flushPromises()
    btn = w.findAll('button').find((b) => b.text() === '设为当前')
    expect(btn!.attributes('disabled')).toBeUndefined()
    w.unmount()
  })

  it('当前档案显示「当前档案」章而非激活按钮', async () => {
    h.getAvatar.mockResolvedValue(detail({ avatar_asset_id: 'a1', is_active: true }))
    const w = mount(AvatarEditPage)
    await flushPromises()
    expect(w.find('.pill-active').text()).toBe('当前档案')
    expect(w.findAll('button').some((b) => b.text() === '设为当前')).toBe(false)
  })

  it('焦点环/溢出样式存在性（静态断言，Tab 序走查按 v3 §10 人工清单）', () => {
    const raw = readFileSync('src/pages/AvatarEditPage.vue', 'utf-8')
    expect(raw).toContain(':focus-visible')
    expect(raw).toContain('overflow: hidden')
  })
})
