import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const h = vi.hoisted(() => ({
  sessionId: '',
  push: vi.fn(),
  listSessions: vi.fn(),
  costSummary: vi.fn(),
  getDanmakuState: vi.fn(),
  getSession: vi.fn()
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => ({ params: { id: h.sessionId } }),
    useRouter: () => ({ push: h.push })
  }
})

vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return {
    ...actual,
    listSessions: h.listSessions,
    costSummary: h.costSummary,
    getDanmakuState: h.getDanmakuState,
    getSession: h.getSession
  }
})

import SessionsPage from '../SessionsPage.vue'

const session = {
  id: 'session-1',
  phase: 'ended',
  mode: 'live',
  started_at_ms: 1,
  ended_at_ms: 2,
  duration_s: 12,
  recording_state: 'none',
  updated_at_ms: 2
}

describe('独立场次复盘页', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    h.sessionId = ''
    h.listSessions.mockResolvedValue([session])
    h.costSummary.mockResolvedValue({ group_by: 'session', totals: [] })
    h.getDanmakuState.mockResolvedValue({ state: { mode: 'off', enabled: false, top: [] }, top_scope: 'calendar_day' })
    h.getSession.mockResolvedValue({ session, events: [], event_count: 0 })
  })

  it('列表选择场次后导航到独立详情路由', async () => {
    const w = mount(SessionsPage)
    await flushPromises()

    await w.find('.session').trigger('click')
    expect(h.push).toHaveBeenCalledWith('/sessions/session-1')
    expect(w.findComponent({ name: 'PanelShell' }).exists()).toBe(false)
    w.unmount()
  })

  it('直接打开详情路由时加载该场详情，并可返回列表', async () => {
    h.sessionId = 'session-1'
    const w = mount(SessionsPage)
    await flushPromises()

    expect(h.getSession).toHaveBeenCalledWith('session-1')
    expect(w.text()).toContain('session-1')
    expect(w.find('[data-testid="storage-warning"]').text()).toContain('重启')
    await w.find('[data-testid="sessions-back"]').trigger('click')
    expect(h.push).toHaveBeenCalledWith('/sessions')
    w.unmount()
  })
})
