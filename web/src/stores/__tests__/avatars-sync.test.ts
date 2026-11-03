import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { AvatarDetail } from '../../services/api/panels'

const h = vi.hoisted(() => ({
  listAvatars: vi.fn(),
  getAvatar: vi.fn(),
  syncAvatarToVidu: vi.fn()
}))

vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return {
    ...actual,
    listAvatars: (...args: unknown[]) => h.listAvatars(...args),
    getAvatar: (...args: unknown[]) => h.getAvatar(...args),
    syncAvatarToVidu: (...args: unknown[]) => h.syncAvatarToVidu(...args)
  }
})

import { useAvatarsStore } from '../avatars'

// R3-02 最小验证：syncAndWait 支持 AbortSignal——组件卸载 abort 后轮询立即停止，
// 不再跑满 480s 上限；syncing 防连点标记在 finally 中清理。
function detail(state: string): AvatarDetail {
  return {
    persona_id: 'p1',
    slug: 'xiao-ya',
    name: '小雅',
    system_prompt: '',
    greeting: '',
    voice_id: '',
    refs: [],
    vidu_sync_state: { state },
    is_active: false,
    revision: 1
  }
}

describe('avatars store syncAndWait（R3-02）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    h.listAvatars.mockResolvedValue({ avatars: [], total: 0, storage: 'sqlite' })
    h.syncAvatarToVidu.mockResolvedValue({ job_id: 'j1' })
  })

  it('AbortSignal 中断轮询：抛 AbortError 并清理 syncing 标记', async () => {
    h.getAvatar.mockResolvedValue(detail('uploading'))
    const store = useAvatarsStore()
    const ctrl = new AbortController()
    const p = store.syncAndWait('p1', ctrl.signal)
    // 等首轮 sync 提交完成、sleep 挂起
    await new Promise((r) => setTimeout(r, 10))
    expect(store.syncing['p1']).toBe(true)
    ctrl.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
    expect(store.syncing['p1']).toBeUndefined()
    expect(h.syncAvatarToVidu).toHaveBeenCalledTimes(1)
  })

  it('终态 done 立即返回，不再继续轮询', async () => {
    vi.useFakeTimers()
    try {
      h.getAvatar.mockResolvedValue(detail('done'))
      const store = useAvatarsStore()
      const p = store.syncAndWait('p1')
      await vi.advanceTimersByTimeAsync(2000)
      await expect(p).resolves.toMatchObject({ state: 'done' })
      expect(h.getAvatar).toHaveBeenCalledTimes(1)
      expect(store.syncing['p1']).toBeUndefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('同 id 重复提交抛 sync_in_flight（防连点）', async () => {
    h.getAvatar.mockResolvedValue(detail('uploading'))
    const store = useAvatarsStore()
    const ctrl = new AbortController()
    const p1 = store.syncAndWait('p1', ctrl.signal)
    await new Promise((r) => setTimeout(r, 10))
    await expect(store.syncAndWait('p1')).rejects.toMatchObject({ code: 'sync_in_flight' })
    ctrl.abort()
    await expect(p1).rejects.toMatchObject({ name: 'AbortError' })
  })
})
