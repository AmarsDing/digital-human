import { describe, expect, it } from 'vitest'
import { mount, shallowMount } from '@vue/test-utils'
import VideoStage from '../VideoStage.vue'
import LiveStageBadge from '../LiveStageBadge.vue'

const baseProps = {
  phase: 'live',
  liveId: 'live-1',
  durationS: 65,
  creds: null,
  wsState: '在线',
  lagMs: 0,
  feedOwner: true,
  degraded: false,
  caption: '',
  captionsEnabled: true,
  captionFontSize: 17,
  captionMaxLines: 2,
  captionOffsetPx: 24,
  busy: false,
  error: ''
}

describe('VideoStage 直播状态角标', () => {
  it('LIVE 与时长贴在画面内，标题栏不再重复时长', () => {
    const w = shallowMount(VideoStage, {
      props: baseProps,
      global: { stubs: { RtcPlayer: true } }
    })
    const badge = w.get('.stage__viewport').findComponent(LiveStageBadge)
    expect(badge.exists()).toBe(true)
    expect(badge.props('status')).toBe('live')
    expect(badge.props('elapsedSeconds')).toBe(65)
    expect(badge.attributes('data-phase')).toBe('live')
    expect(w.find('.stage__dur').exists()).toBe(false)
  })

  it('非直播阶段保留阶段标签但不显示 LIVE 角标', async () => {
    const w = shallowMount(VideoStage, {
      props: baseProps,
      global: { stubs: { RtcPlayer: true } }
    })
    await w.setProps({ phase: 'ended', durationS: 0 })
    expect(w.findComponent(LiveStageBadge).exists()).toBe(false)
    expect(w.get('.stage__pill').attributes('data-phase')).toBe('ended')
  })
})

describe('VideoStage 开播控制条', () => {
  function mountStage(props: Record<string, unknown> = {}) {
    return mount(VideoStage, {
      props: { ...baseProps, ...props },
      global: { stubs: { RtcPlayer: true } }
    })
  }

  it('准备态预检入口独立触发 preflight，开播按钮使用 primary', async () => {
    const w = mountStage({ phase: 'idle', liveId: '', durationS: 0 })
    await w.get('.stage__preflight').trigger('click')
    expect(w.emitted('preflight')).toHaveLength(1)
    expect(w.emitted('start')).toBeUndefined()
    expect(w.get('.stage__start').classes()).toContain('nt-btn--primary')
  })

  it('本次会话摘要只按 check.status 计数，并区分检测中', async () => {
    const w = mountStage({
      phase: 'idle',
      preflight: {
        ready: false,
        checks: [
          { status: 'fail' },
          { status: 'warn' },
          { status: 'unknown' },
          { status: 'running' },
          { status: 'pass', detail: 'fail warn unknown' }
        ]
      }
    })
    const summary = w.get('.stage__preflight-summary')
    expect(summary.text()).toContain('本次会话')
    expect(summary.text()).toContain('失败 1')
    expect(summary.text()).toContain('提醒 1')
    expect(summary.text()).toContain('待验证 2')
    await w.setProps({ preflightBusy: true })
    expect(summary.text()).toContain('检测中')
  })

  it('开始/停止触发原事件，停止按钮使用 danger', async () => {
    const w = mountStage({ phase: 'idle', liveId: '', durationS: 0 })
    await w.get('.stage__start').trigger('click')
    expect(w.emitted('start')).toHaveLength(1)
    await w.setProps({ phase: 'live' })
    expect(w.get('.stage__stop').classes()).toContain('nt-btn--danger')
    await w.get('.stage__stop').trigger('click')
    expect(w.emitted('stop')).toHaveLength(1)
  })
})
