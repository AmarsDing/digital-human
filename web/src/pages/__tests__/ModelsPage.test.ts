import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const h = vi.hoisted(() => ({
  capability: {} as Record<string, string>
}))

vi.mock('../../services/api/settings', () => ({
  getSettings: vi.fn(async () => ({ groups: [], revision: 1 })),
  saveSettings: vi.fn()
}))
vi.mock('../../services/api/client', () => ({
  apiRequest: vi.fn(async () => ({ capability_matrix: h.capability })),
  classifyApiError: (err: unknown) => ({ code: 'error', message: String(err) })
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))

import ModelsPage from '../ModelsPage.vue'

describe('ModelsPage 内容终检能力状态', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    h.capability = {}
  })

  it.each([
    ['real', '已启用'],
    ['unconfigured', '未启用'],
    ['unknown', '配置异常']
  ])('%s 显示为%s', async (source, label) => {
    h.capability = { content_gate: source }
    const wrapper = shallowMount(ModelsPage)
    await flushPromises()
    expect(wrapper.get('[data-testid="content-gate-status"]').text()).toContain(label)
    wrapper.unmount()
  })
})
