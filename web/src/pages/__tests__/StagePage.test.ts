import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const h = vi.hoisted(() => ({
  listAvatars: vi.fn(),
  hotkeyStart: undefined as undefined | (() => void),
  unregisterHotkeys: vi.fn(),
  push: vi.fn()
}))

vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return { ...actual, listAvatars: (...args: unknown[]) => h.listAvatars(...args) }
})

vi.mock('../../utils/hotkeys', () => ({
  registerStageHotkeys: (options: { onStart: () => void }) => {
    h.hotkeyStart = options.onStart
    return h.unregisterHotkeys
  }
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRouter: () => ({ push: h.push }) }
})

import StagePage from '../StagePage.vue'
import VideoStage from '../../components/stage/VideoStage.vue'
import { useLiveStore } from '../../stores/live'
import { useAvatarsStore } from '../../stores/avatars'

function activeAvatar(revision: number) {
  return { avatars: [{ persona_id: 'p1', is_active: true, revision }], total: 1, storage: 'sqlite' }
}

function offlineFetch(): Promise<Response> {
  return Promise.resolve({ ok: false, status: 503, text: async () => '{}' } as Response)
}

describe('StagePage 统一 preflight 开播入口', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    h.hotkeyStart = undefined
    vi.stubGlobal('fetch', vi.fn(offlineFetch))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('F9 固定 active persona/revision，并在预检后复核同一选择', async () => {
    h.listAvatars.mockResolvedValue(activeAvatar(7))
    const live = useLiveStore()
    let recheck: (() => boolean | Promise<boolean>) | undefined
    const start = vi.spyOn(live, 'start').mockImplementation(async (_input, guard) => {
      recheck = guard
      return true
    })
    const w = shallowMount(StagePage)
    await flushPromises()

    h.hotkeyStart!()
    await flushPromises()

    expect(start).toHaveBeenCalledWith(
      { personaId: 'p1', draftId: '', expectedRevision: 0 },
      expect.any(Function)
    )
    expect(recheck).toBeTypeOf('function')
    expect(await recheck!()).toBe(true)
    expect(h.listAvatars).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it('预检后 active revision 改变时复核失败', async () => {
    h.listAvatars.mockResolvedValueOnce(activeAvatar(7)).mockResolvedValueOnce(activeAvatar(8))
    const live = useLiveStore()
    let recheck: (() => boolean | Promise<boolean>) | undefined
    vi.spyOn(live, 'start').mockImplementation(async (_input, guard) => {
      recheck = guard
      return false
    })
    const w = shallowMount(StagePage)
    await flushPromises()

    w.findComponent(VideoStage).vm.$emit('start')
    await flushPromises()

    expect(recheck).toBeTypeOf('function')
    expect(await recheck!()).toBe(false)
    w.unmount()
  })

  it('重新同步后只进入一次统一 start，不因 avatar_expired 自动重试', async () => {
    h.listAvatars.mockResolvedValue(activeAvatar(7))
    const live = useLiveStore()
    const start = vi.spyOn(live, 'start').mockImplementation(async () => {
      live.errorCode = 'avatar_expired'
      return false
    })
    const avatars = useAvatarsStore()
    const sync = vi.spyOn(avatars, 'syncAndWait').mockResolvedValue({ state: 'done' } as never)
    const w = shallowMount(StagePage)
    await flushPromises()

    w.findComponent(VideoStage).vm.$emit('start')
    await flushPromises()
    const resync = w.findAll('button').find((b) => b.text().includes('重新同步并开播'))
    expect(resync).toBeDefined()
    await resync!.trigger('click')
    await flushPromises()

    expect(sync).toHaveBeenCalledTimes(1)
    expect(start).toHaveBeenCalledTimes(2)
    w.unmount()
  })
})
