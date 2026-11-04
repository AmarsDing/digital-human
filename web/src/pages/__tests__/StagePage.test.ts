import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'
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
import { useEventsStore } from '../../stores/events'
import type { WireEvent } from '../../services/api/types'
import { useAvatarsStore } from '../../stores/avatars'
import { useUiPrefs } from '../../stores/uiPrefs'
import PanelDock from '../../components/stage/PanelDock.vue'
import PanelShell from '../../components/panels/PanelShell.vue'
import NtConfirmDialog from '../../components/ui/NtConfirmDialog.vue'

function activeAvatar(revision: number) {
  return { avatars: [{ persona_id: 'p1', is_active: true, revision }], total: 1, storage: 'sqlite' }
}

function offlineFetch(): Promise<Response> {
  return Promise.resolve({ ok: false, status: 503, text: async () => '{}' } as Response)
}

function stageEvent(type: string, seq: number, payload: Record<string, unknown>): WireEvent {
  return { seq, event_id: `e-${seq}`, session_id: 's1', type, ts_ms: 1000 + seq, epoch: 1, payload }
}

describe('StagePage 双模式与单工作区', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('fetch', vi.fn(offlineFetch))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('旧浮层偏好的话也只渲染一个工作区，复盘使用独立路由', async () => {
    const prefs = useUiPrefs()
    prefs.panels.voice.open = true
    const w = shallowMount(StagePage)
    await flushPromises()

    expect(w.findComponent(PanelDock).exists()).toBe(true)
    expect(w.findAllComponents(PanelShell)).toHaveLength(0)
    const sessions = w.find('[data-testid="sessions-link"]')
    expect(sessions.exists()).toBe(true)
    await sessions.trigger('click')
    expect(h.push).toHaveBeenCalledWith('/sessions')
    w.unmount()
  })

  it('直播中显示冻结的当场模式，准备态显示下一场配置口径', async () => {
    const live = useLiveStore()
    live.status = { phase: 'live' } as never
    const w = shallowMount(StagePage)
    await flushPromises()
    expect(w.find('.stage-mode').text()).toContain('直播中')
    expect(w.find('.stage-mode').text()).toContain('本场已冻结')
    w.unmount()
  })
})

describe('StagePage 歌词字幕仲裁', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('fetch', vi.fn(offlineFetch))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('歌词优先于口播，切歌时完整替换旧歌词', async () => {
    const events = useEventsStore()
    const w = shallowMount(StagePage)
    await flushPromises()
    const stage = w.findComponent(VideoStage)

    events.accept(stageEvent('transcript.final', 1, { text: '口播一', index: 0 }))
    await flushPromises()
    expect(stage.props('caption')).toBe('口播一')

    events.accept(stageEvent('song.playing', 2, { song_playing: true, caption_lines: ['歌词一', '歌词二'] }))
    await flushPromises()
    expect(stage.props('caption')).toBe('歌词一\n歌词二')

    events.accept(stageEvent('transcript.final', 3, { text: '口播二', index: 1 }))
    await flushPromises()
    expect(stage.props('caption')).toBe('歌词一\n歌词二')

    events.accept(stageEvent('song.playing', 4, { song_playing: true, caption_lines: ['切歌歌词'] }))
    await flushPromises()
    expect(stage.props('caption')).toBe('切歌歌词')
    w.unmount()
  })

  it.each(['song.stopped', 'song.ended'])('%s 后清除歌词并恢复当前口播，不残留旧字幕', async (type) => {
    const events = useEventsStore()
    const w = shallowMount(StagePage)
    await flushPromises()
    const stage = w.findComponent(VideoStage)

    events.accept(stageEvent('transcript.final', 1, { text: '当前口播', index: 0 }))
    events.accept(stageEvent('song.playing', 2, { song_playing: true, caption_lines: ['旧歌词'] }))
    await flushPromises()
    expect(stage.props('caption')).toBe('旧歌词')

    events.accept(stageEvent(type, 3, { song_playing: false }))
    await flushPromises()
    expect(stage.props('caption')).toBe('当前口播')
    expect(stage.props('caption')).not.toContain('旧歌词')
    w.unmount()
  })
})

describe('PanelDock 工作区 Tabs', () => {
  it('方向键只移动焦点，Enter 才激活目标面板', async () => {
    const w = mount(PanelDock, {
      attachTo: document.body,
      props: { active: 'voice', pendingCount: 0, rightOpen: true }
    })
    const tabs = w.findAll('[role="tab"]')
    expect(tabs.length).toBe(6)
    await tabs[0].trigger('focus')
    await tabs[0].trigger('keydown', { key: 'ArrowRight' })
    expect(w.emitted('select')).toBeUndefined()
    expect(document.activeElement).toBe(tabs[1].element)
    await tabs[1].trigger('keydown', { key: 'Enter' })
    expect(w.emitted('select')).toEqual([['command']])
    w.unmount()
  })
})

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
      { personaId: 'p1' },
      expect.any(Function)
    )
    expect(recheck).toBeTypeOf('function')
    expect(await recheck!()).toBe(true)
    expect(h.listAvatars).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it('预检缺字段时把具体字段错误传给开播 UI', async () => {
    h.listAvatars.mockResolvedValue(activeAvatar(7))
    const live = useLiveStore()
    vi.spyOn(live, 'start').mockImplementation(async () => {
      live.errorCode = 'preflight_contract_missing'
      live.error = '开播预检缺少字段：personaId、revision'
      return false
    })
    const w = shallowMount(StagePage)
    await flushPromises()

    w.findComponent(VideoStage).vm.$emit('start')
    await flushPromises()

    expect(w.findComponent(VideoStage).props('error')).toContain('personaId、revision')
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

  it('清空字幕使用危险确认并在请求期间防重入', async () => {
    const live = useLiveStore()
    let release: () => void = () => undefined
    const clear = vi.spyOn(live, 'clearTranscripts').mockImplementation(async () => {
      live.busy = true
      await new Promise<void>((resolve) => { release = resolve })
      live.busy = false
      return true
    })
    const w = shallowMount(StagePage)
    await flushPromises()
    const dialog = w.findAllComponents(NtConfirmDialog).find((d) => d.props('title') === '清空字幕')!

    expect(dialog.props('dangerous')).toBe(true)
    dialog.vm.$emit('confirm')
    await flushPromises()
    expect(dialog.props('busy')).toBe(true)
    dialog.vm.$emit('confirm')
    expect(clear).toHaveBeenCalledTimes(1)
    release()
    await flushPromises()
    expect(dialog.props('open')).toBe(false)
    w.unmount()
  })

  it('profile_changed 引导前往档案页确认改动', async () => {
    const live = useLiveStore()
    const w = shallowMount(StagePage)
    await flushPromises()
    // 避开挂载时离线 load 的错误覆盖，模拟 Start 之后写入的拒绝状态。
    live.errorCode = 'profile_changed'
    live.error = '预检后数字人档案或版本已变化，请前往档案页确认改动后重新预检'
    await flushPromises()

    const guide = w.find('.guide-banner')
    expect(guide.text()).toContain('确认改动')
    await guide.get('button').trigger('click')
    expect(h.push).toHaveBeenCalledWith('/avatars')
    w.unmount()
  })

  it('preflight_expired 提供重新预检动作并重新进入统一 start', async () => {
    h.listAvatars.mockResolvedValue(activeAvatar(7))
    const live = useLiveStore()
    live.errorCode = 'preflight_expired'
    live.error = '开播预检凭证已过期，请重新预检后再开播'
    const start = vi.spyOn(live, 'start').mockResolvedValue(true)
    const w = shallowMount(StagePage)
    await flushPromises()

    const retry = w.findAll('button').find((b) => b.text() === '重新预检并开播')
    expect(retry).toBeDefined()
    await retry!.trigger('click')
    await flushPromises()
    expect(start).toHaveBeenCalledWith(
      { personaId: 'p1' },
      expect.any(Function)
    )
    expect(h.push).not.toHaveBeenCalledWith('/avatars')
    w.unmount()
  })

  it('preflight_required 提供重新预检动作并作废旧预检', async () => {
    h.listAvatars.mockResolvedValue(activeAvatar(7))
    const live = useLiveStore()
    live.errorCode = 'preflight_required'
    live.error = '开播预检凭证缺失、未知、过期或已消费，请重新预检后再开播'
    const start = vi.spyOn(live, 'start').mockResolvedValue(true)
    const w = shallowMount(StagePage)
    await flushPromises()

    const retry = w.findAll('button').find((b) => b.text() === '重新预检并开播')
    expect(retry).toBeDefined()
    await retry!.trigger('click')
    await flushPromises()
    expect(start).toHaveBeenCalledTimes(1)
    expect(start).toHaveBeenCalledWith({ personaId: 'p1' }, expect.any(Function))
    w.unmount()
  })

  it('publish_target_unverified 引导平台接入验证，不在直播台自动重试', async () => {
    const live = useLiveStore()
    live.errorCode = 'publish_target_unverified'
    live.error = '真实抖音推流目标尚未验证，请前往平台接入完成真实推流验证后重新预检'
    const start = vi.spyOn(live, 'start').mockResolvedValue(true)
    const w = shallowMount(StagePage)
    await flushPromises()

    const guide = w.find('.guide-banner')
    const jump = guide.findAll('button').find((b) => b.text() === '前往平台接入验证')
    expect(jump).toBeDefined()
    await jump!.trigger('click')
    expect(h.push).toHaveBeenCalledWith('/platform')
    expect(start).not.toHaveBeenCalled()
    w.unmount()
  })

  it('draft_required 引导先选择并提交草稿，不用空草稿自动重试', async () => {
    const live = useLiveStore()
    live.errorCode = 'draft_required'
    live.error = 'public-live 必须先选择并提交草稿，再携带草稿 ID 与版本重新预检；不能使用空草稿开播'
    const start = vi.spyOn(live, 'start').mockResolvedValue(true)
    const w = shallowMount(StagePage)
    await flushPromises()

    const guide = w.find('.guide-banner')
    expect(guide.text()).toContain('选择并提交草稿')
    expect(guide.text()).toContain('草稿列表接口')
    expect(w.findAll('button').some((b) => b.text() === '重新预检并开播')).toBe(false)
    expect(start).not.toHaveBeenCalled()
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
