import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import PlatformPage from '../pages/PlatformPage.vue'

// 后端不在测试里：全部走 mock fetch，验证三态、密钥只写不回显、轮询与保存载荷。

type Handler = (init?: RequestInit) => { status?: number; body?: unknown }

const STREAM_OK = {
  state: 'idle',
  bitrate_kbps: 0,
  fps: 0,
  dropped_frames: 0,
  key_fingerprint8: '',
  last_error: '',
  platform: 'douyin'
}
const COLLECTOR_OK = { state: 'off', room_id: '', last_msg_ms: 0, msgs_per_min: 0 }

function makeFetch(handlers: Record<string, Handler>) {
  return vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    for (const [key, h] of Object.entries(handlers)) {
      if (url.includes(key)) {
        const r = h(init)
        const status = r.status ?? 200
        return Promise.resolve({
          ok: status >= 200 && status < 300,
          status,
          json: async () => r.body ?? {},
          text: async () => JSON.stringify(r.body ?? {})
        } as Response)
      }
    }
    return Promise.resolve({ ok: false, status: 404, json: async () => ({}), text: async () => '{}' } as Response)
  })
}

function okFetch(stream: Record<string, unknown> = STREAM_OK, collector: Record<string, unknown> = COLLECTOR_OK) {
  return makeFetch({
    '/api/v1/stream/status': () => ({ body: stream }),
    '/api/v1/collector/status': () => ({ body: collector }),
    '/api/v1/stream/target': () => ({ body: { ok: true, key_fingerprint8: 'abcd1234' } }),
    '/api/v1/stream/start': () => ({ body: { ok: true, reason: '' } }),
    '/api/v1/stream/stop': () => ({ body: { ok: true } }),
    '/api/v1/collector/config': () => ({ body: { ok: true } })
  })
}

async function mountPage(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/platform', component: PlatformPage }]
  })
  await router.push('/platform')
  await router.isReady()
  const wrapper = mount(PlatformPage, {
    attachTo: document.body,
    global: { plugins: [createPinia(), router] }
  })
  await flushPromises()
  return wrapper
}

function callsOf(fetchMock: ReturnType<typeof makeFetch>, path: string) {
  return fetchMock.mock.calls.filter((c) => String(c[0]).includes(path))
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('平台接入页', () => {
  it('加载态不白屏：fetch 未返回时展示加载中', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise<Response>(() => undefined))
    )
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/platform', component: PlatformPage }]
    })
    await router.push('/platform')
    await router.isReady()
    const wrapper = await mount(PlatformPage, {
      attachTo: document.body,
      global: { plugins: [createPinia(), router] }
    })
    expect(wrapper.find('.platform-page').exists()).toBe(true)
    expect(wrapper.text()).toContain('加载中')
    wrapper.unmount()
  })

  it('错误态不白屏：503 时各卡片内联 role=alert 与重试', async () => {
    vi.stubGlobal(
      'fetch',
      makeFetch({
        '/api/v1/stream/status': () => ({ status: 503, body: { error: 'unavailable', message: '服务不可用' } }),
        '/api/v1/collector/status': () => ({ status: 503, body: { error: 'unavailable', message: '服务不可用' } })
      })
    )
    const wrapper = await mountPage()
    const alerts = wrapper.findAll('[role="alert"]')
    expect(alerts.length).toBeGreaterThanOrEqual(2)
    expect(wrapper.text()).toContain('服务不可用')
    expect(wrapper.text()).toContain('重试')
    wrapper.unmount()
  })

  it('状态胶囊文案随 state 变化（idle → 未推流）', async () => {
    vi.stubGlobal('fetch', okFetch())
    const wrapper = await mountPage()
    expect(wrapper.text()).toContain('未推流')
    const startBtn = wrapper.findAll('button').find((b) => b.text() === '开始推流')
    expect(startBtn?.attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[aria-live="polite"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('真实抖音 pushing 时胶囊为推流中，开始按钮禁用、停止可用', async () => {
    vi.stubGlobal('fetch', okFetch({ ...STREAM_OK, state: 'pushing', target_verification: 'douyin', bitrate_kbps: 2500, fps: 30 }))
    const wrapper = await mountPage()
    expect(wrapper.text()).toContain('推流中')
    expect(wrapper.find('[aria-label="推流控制"] .capsule--success').exists()).toBe(true)
    expect(wrapper.text()).toContain('2500')
    const startBtn = wrapper.findAll('button').find((b) => b.text() === '开始推流')
    const stopBtn = wrapper.findAll('button').find((b) => b.text() === '停止推流')
    expect(startBtn?.attributes('disabled')).toBeDefined()
    expect(stopBtn?.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('本地 MediaMTX pushing 只显示本地协议验证，不渲染为可用', async () => {
    vi.stubGlobal('fetch', okFetch({ ...STREAM_OK, state: 'pushing', target_verification: 'local' }))
    const wrapper = await mountPage()
    const capsule = wrapper.find('[aria-label="推流控制"] .capsule')
    expect(capsule.text()).toBe('仅本地协议验证')
    expect(capsule.classes()).toContain('capsule--warn')
    expect(wrapper.find('[aria-label="推流控制"] .capsule--success').exists()).toBe(false)
    wrapper.unmount()
  })

  it('未识别目标 pushing 显示平台未验证，不渲染为可用', async () => {
    vi.stubGlobal('fetch', okFetch({ ...STREAM_OK, state: 'pushing', target_verification: 'unknown' }))
    const wrapper = await mountPage()
    const capsule = wrapper.find('[aria-label="推流控制"] .capsule')
    expect(capsule.text()).toBe('平台未验证')
    expect(capsule.classes()).toContain('capsule--warn')
    expect(wrapper.find('[aria-label="推流控制"] .capsule--success').exists()).toBe(false)
    wrapper.unmount()
  })

  it('弹幕源 degraded 时内联提示 role=alert', async () => {
    vi.stubGlobal('fetch', okFetch(STREAM_OK, { ...COLLECTOR_OK, state: 'degraded', room_id: '888' }))
    const wrapper = await mountPage()
    expect(wrapper.text()).toContain('已降级')
    const alert = wrapper.findAll('[role="alert"]').find((a) => a.text().includes('弹幕源异常，可在舞台页手动互动'))
    expect(alert).toBeTruthy()
    wrapper.unmount()
  })

  it('stream key 只写不回显：提交后清空且 DOM 不留明文，载荷正确', async () => {
    const fetchMock = okFetch({ ...STREAM_OK, key_fingerprint8: 'abcd1234' })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = await mountPage()

    expect(wrapper.text()).toContain('已配置 · 指纹 abcd1234')
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)

    await wrapper.findAll('button').find((b) => b.text() === '更换')!.trigger('click')
    const urlInput = wrapper.find('input[type="text"]')
    const keyInput = wrapper.find('input[type="password"]')
    await urlInput.setValue('rtmp://live.example.com/app')
    await keyInput.setValue('supersecret')
    await wrapper.findAll('button').find((b) => b.text() === '保存推流配置')!.trigger('click')
    await flushPromises()

    // 提交后输入框被清空、明文不再出现在 DOM
    expect(wrapper.html()).not.toContain('supersecret')
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)

    const targetCalls = callsOf(fetchMock, '/api/v1/stream/target')
    expect(targetCalls.length).toBe(1)
    const payload = JSON.parse(String(targetCalls[0][1]?.body)) as Record<string, unknown>
    expect(payload.platform).toBe('douyin')
    expect(payload.rtmp_url).toBe('rtmp://live.example.com/app')
    expect(payload.stream_key).toBe('supersecret')
    expect(typeof payload.idempotency_key).toBe('string')
    expect(String(payload.idempotency_key).length).toBeGreaterThan(0)

    // 再次展开更换时密码框为空
    await wrapper.findAll('button').find((b) => b.text() === '更换')!.trigger('click')
    expect((wrapper.find('input[type="password"]').element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })

  it('轮询：mount 即拉取状态，2s 周期续拉，unmount 后不再发', async () => {
    vi.useFakeTimers()
    const fetchMock = okFetch()
    vi.stubGlobal('fetch', fetchMock)
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/platform', component: PlatformPage }]
    })
    await router.push('/platform')
    await router.isReady()
    const wrapper = mount(PlatformPage, {
      attachTo: document.body,
      global: { plugins: [createPinia(), router] }
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(callsOf(fetchMock, '/api/v1/stream/status').length).toBe(1)

    await vi.advanceTimersByTimeAsync(2000)
    expect(callsOf(fetchMock, '/api/v1/stream/status').length).toBe(2)
    await vi.advanceTimersByTimeAsync(2000)
    expect(callsOf(fetchMock, '/api/v1/stream/status').length).toBe(3)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(6000)
    expect(callsOf(fetchMock, '/api/v1/stream/status').length).toBe(3)
  })

  it('房号保存调用 collector/config，payload 正确', async () => {
    const fetchMock = okFetch()
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = await mountPage()

    const roomInput = wrapper.find('input[inputmode="numeric"]')
    await roomInput.setValue('123456')
    await wrapper.findAll('button').find((b) => b.text() === '保存')!.trigger('click')
    await flushPromises()

    const calls = callsOf(fetchMock, '/api/v1/collector/config')
    expect(calls.length).toBe(1)
    const payload = JSON.parse(String(calls[0][1]?.body)) as Record<string, unknown>
    expect(payload).toMatchObject({ platform: 'douyin', room_id: '123456', relay_mode: 'external' })
    expect(typeof payload.idempotency_key).toBe('string')
    expect(wrapper.text()).toContain('已保存')
    wrapper.unmount()
  })

  it('合规提示与平台说明固定在页面中', async () => {
    vi.stubGlobal('fetch', okFetch())
    const wrapper = await mountPage()
    expect(wrapper.text()).toContain('抖音平台侧开播需在抖音直播伴侣/App 手动完成')
    expect(wrapper.text()).toContain('AI 数字人直播需在直播间显著标识 AI 生成内容')
    wrapper.unmount()
  })
})
