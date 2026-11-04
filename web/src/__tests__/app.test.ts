import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import App from '../App.vue'
import StagePage from '../pages/StagePage.vue'
import piniaBoot from '../boot/pinia'
import quasarConfig from '../../quasar.config.js'

// 后端不在测试里：所有请求返回 503，验证的是“壳与页面能挂载”，不是数据。
// 统一返回 Response 形状，避免 App 内未捕获的 fetch 拒绝污染测试运行。
function offlineFetch(): Promise<Response> {
  return Promise.resolve({
    ok: false,
    status: 503,
    json: async () => ({}),
    text: async () => '{}'
  } as Response)
}

const AppRoot = defineComponent({
  components: { App },
  template: '<div id="q-app"><App /></div>'
})

async function mountApp() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: StagePage }]
  })
  await router.push('/')
  await router.isReady()
  const wrapper = mount(AppRoot, {
    attachTo: document.body,
    global: {
      plugins: [createPinia(), router],
      stubs: { RouterView: false }
    }
  })
  await flushPromises()
  return wrapper
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('应用挂载（P0 白屏防回归）', () => {
  it('#q-app 内渲染出根节点与舞台关键元素', async () => {
    vi.stubGlobal('fetch', vi.fn(offlineFetch))
    const wrapper = await mountApp()

    expect(wrapper.find('#q-app').exists()).toBe(true)
    expect(wrapper.find('#q-app .dh-shell').exists()).toBe(true)
    expect(wrapper.find('#q-app .page').exists()).toBe(true)
    expect(wrapper.find('#q-app .stage').exists()).toBe(true)
    expect(wrapper.find('#q-app .dh-top').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('小雅 · 数字人直播台')
    expect(wrapper.text()).toContain('开始直播')

    wrapper.unmount()
  })

  it('quasar boot 装配 pinia，boot 模块会向 app 安装 Pinia 插件', () => {
    const cfg = (quasarConfig as unknown as (ctx: unknown) => { boot?: string[] })({})
    expect(cfg.boot).toContain('pinia')

    const use = vi.fn()
    piniaBoot({ app: { use } })
    expect(use).toHaveBeenCalledTimes(1)
    const plugin = use.mock.calls[0][0] as { install?: unknown }
    expect(typeof plugin.install).toBe('function')
  })
})
