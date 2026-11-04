import { describe, expect, it } from 'vitest'
import { shallowMount } from '@vue/test-utils'
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
