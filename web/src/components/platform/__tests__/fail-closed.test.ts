import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const h = vi.hoisted(() => ({
  getStreamStatus: vi.fn(), getCollectorStatus: vi.fn(), saveStreamTarget: vi.fn(),
  saveCollectorConfig: vi.fn(), startStream: vi.fn(), stopStream: vi.fn()
}))
vi.mock('../../../services/api/platform', async () => {
  const actual = await vi.importActual<typeof import('../../../services/api/platform')>('../../../services/api/platform')
  return {
    ...actual, getStreamStatus: h.getStreamStatus, getCollectorStatus: h.getCollectorStatus,
    saveStreamTarget: h.saveStreamTarget, saveCollectorConfig: h.saveCollectorConfig,
    startStream: h.startStream, stopStream: h.stopStream
  }
})

import { ApiError } from '../../../services/api/client'
import { usePlatformStore } from '../../../stores/platform'
import StreamControlCard from '../StreamControlCard.vue'
import StreamTargetCard from '../StreamTargetCard.vue'
import DanmakuSourceCard from '../DanmakuSourceCard.vue'

const stream = {
  state: 'idle' as const, targetVerification: 'unknown' as const, bitrateKbps: 0,
  fps: 0, droppedFrames: 0, keyFingerprint8: '', lastError: '', platform: 'douyin'
}
const collector = { state: 'off' as const, roomId: '', lastMsgMs: 0, msgsPerMin: 0 }

describe('平台能力 fail-closed', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setActivePinia(createPinia())
  })

  it('404 标记能力不可用，恢复成功后才重新开放', async () => {
    h.getStreamStatus.mockRejectedValueOnce(new ApiError(404, 'not_found', 'missing')).mockResolvedValueOnce(stream)
    h.getCollectorStatus.mockRejectedValueOnce(new ApiError(404, 'not_found', 'missing')).mockResolvedValueOnce(collector)
    const store = usePlatformStore()
    await store.refreshStream()
    await store.refreshCollector()
    expect(store.streamUnavailable).toBe(true)
    expect(store.collectorUnavailable).toBe(true)
    expect(store.streamError).toContain('尚未启用推流')
    expect(store.collectorError).toContain('尚未启用弹幕采集')

    await store.refreshStream()
    await store.refreshCollector()
    expect(store.streamUnavailable).toBe(false)
    expect(store.collectorUnavailable).toBe(false)
  })

  it('推流不可用时禁用开始和目标保存，点击也不调用动作', async () => {
    h.getStreamStatus.mockRejectedValue(new ApiError(404, 'not_found', 'missing'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = usePlatformStore()
    await store.refreshStream()
    const start = vi.spyOn(store, 'start')
    const save = vi.spyOn(store, 'saveTarget')
    const control = mount(StreamControlCard, { global: { plugins: [pinia] } })
    const target = mount(StreamTargetCard, { global: { plugins: [pinia] } })
    await target.get('input[type="text"]').setValue('rtmp://example.com/live')
    await target.get('input[type="password"]').setValue('secret')

    const startButton = control.findAll('button').find((button) => button.text() === '开始推流')!
    const saveButton = target.findAll('button').find((button) => button.text() === '保存推流配置')!
    expect(startButton.attributes('disabled')).toBeDefined()
    expect(saveButton.attributes('disabled')).toBeDefined()
    await startButton.trigger('click')
    await saveButton.trigger('click')
    expect(start).not.toHaveBeenCalled()
    expect(save).not.toHaveBeenCalled()
    control.unmount()
    target.unmount()
  })

  it('弹幕采集不可用时禁用房号保存，Enter 也不能绕过', async () => {
    h.getCollectorStatus.mockRejectedValue(new ApiError(404, 'not_found', 'missing'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = usePlatformStore()
    await store.refreshCollector()
    const saveRoom = vi.spyOn(store, 'saveRoom')
    const w = mount(DanmakuSourceCard, { global: { plugins: [pinia] } })
    const input = w.get('input[placeholder="直播间号"]')
    await input.setValue('123456')
    const save = w.findAll('button').find((button) => button.text() === '保存')!

    expect(save.attributes('disabled')).toBeDefined()
    await input.trigger('keyup', { key: 'Enter' })
    await save.trigger('click')
    await flushPromises()
    expect(saveRoom).not.toHaveBeenCalled()
    w.unmount()
  })
})
