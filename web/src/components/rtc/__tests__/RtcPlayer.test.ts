import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import RtcPlayer from '../RtcPlayer.vue'
import '../adapter'
import type { RtcAdapter, RtcCreds } from '../types'

function creds(overrides: Partial<RtcCreds> = {}): RtcCreds {
  return {
    mode: 'managed',
    provider: 'artc',
    appId: 'app-1',
    roomId: 'dh-s1',
    userId: 'viewer-1',
    token: 'rtc-dev.abc',
    tokenExpireAt: 0,
    ...overrides
  }
}

function fakeAdapter(): RtcAdapter {
  return {
    join: vi.fn(async () => {}),
    leave: vi.fn(async () => {}),
    mute: vi.fn(),
    destroy: vi.fn()
  }
}

async function flush(): Promise<void> {
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

describe('RtcPlayer 末帧保留（G30）', () => {
  let adapter: RtcAdapter

  beforeEach(() => {
    adapter = fakeAdapter()
    window.__DH_RTC_FACTORY__ = { create: () => adapter }
    // jsdom canvas 无 2d 实现：stub 出可截帧环境
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      drawImage: vi.fn()
    } as unknown as CanvasRenderingContext2D)
    vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/jpeg;base64,LASTFRAME')
  })

  afterEach(() => {
    vi.restoreAllMocks()
    delete window.__DH_RTC_FACTORY__
  })

  it('收播（creds 清空）时保留最后一帧铺底，overlay 转玻璃层', async () => {
    const wrapper = mount(RtcPlayer, { props: { creds: creds() } })
    const video = wrapper.get('video').element as HTMLVideoElement
    Object.defineProperty(video, 'videoWidth', { value: 640, configurable: true })
    Object.defineProperty(video, 'videoHeight', { value: 360, configurable: true })
    await flush()
    expect(wrapper.attributes('data-state')).toBe('joined')

    await wrapper.setProps({ creds: null })
    await flush()

    expect(wrapper.attributes('data-state')).toBe('unconfigured')
    const img = wrapper.find('img.rtc__lastframe')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('data:image/jpeg;base64,LASTFRAME')
    expect(wrapper.get('.rtc__overlay').classes()).toContain('rtc__overlay--glass')
  })

  it('重新入会成功后清掉旧帧', async () => {
    const wrapper = mount(RtcPlayer, { props: { creds: creds() } })
    const video = wrapper.get('video').element as HTMLVideoElement
    Object.defineProperty(video, 'videoWidth', { value: 640, configurable: true })
    Object.defineProperty(video, 'videoHeight', { value: 360, configurable: true })
    await flush()

    await wrapper.setProps({ creds: null })
    await flush()
    expect(wrapper.find('img.rtc__lastframe').exists()).toBe(true)

    await wrapper.setProps({ creds: creds({ userId: 'viewer-2' }) })
    await flush()
    expect(wrapper.attributes('data-state')).toBe('joined')
    expect(wrapper.find('img.rtc__lastframe').exists()).toBe(false)
  })

  it('未渲染过帧（videoWidth=0）时不铺底，overlay 保持原样', async () => {
    const wrapper = mount(RtcPlayer, { props: { creds: creds() } })
    await flush()
    await wrapper.setProps({ creds: null })
    await flush()
    expect(wrapper.find('img.rtc__lastframe').exists()).toBe(false)
    expect(wrapper.get('.rtc__overlay').classes()).not.toContain('rtc__overlay--glass')
  })

  it('vendor SDK 缺失：保持明确报错而非静默黑屏', async () => {
    delete window.__DH_RTC_FACTORY__
    const wrapper = mount(RtcPlayer, { props: { creds: creds() } })
    await flush()
    expect(wrapper.attributes('data-state')).toBe('failed')
    expect(wrapper.text()).toContain('RTC SDK 未自托管')
  })
})
