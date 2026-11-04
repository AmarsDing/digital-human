import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import LiveStageBadge from '../LiveStageBadge.vue'

describe('LiveStageBadge', () => {
  it('在同一状态槽展示 LIVE 与完整时长', () => {
    const w = mount(LiveStageBadge, { props: { status: 'live', elapsedSeconds: 3661 } })
    const badge = w.get('[role="status"]')
    expect(badge.text()).toBe('LIVE 01:01:01')
    expect(badge.attributes('data-state')).toBe('live')
  })

  it('不足一分钟也保持 HH:MM:SS 格式', () => {
    const w = mount(LiveStageBadge, { props: { status: 'live', elapsedSeconds: 59 } })
    expect(w.get('[role="status"]').text()).toBe('LIVE 00:00:59')
  })

  it('降级态保留时长并给出可访问状态说明', () => {
    const w = mount(LiveStageBadge, { props: { status: 'degraded', elapsedSeconds: 65 } })
    const badge = w.get('[role="status"]')
    expect(badge.text()).toBe('LIVE · 降级 00:01:05')
    expect(badge.attributes('aria-label')).toContain('直播降级')
    expect(badge.attributes('data-state')).toBe('degraded')
  })

  it('错误态使用同一角标位置而不是新增第二枚角标', () => {
    const w = mount(LiveStageBadge, { props: { status: 'error', elapsedSeconds: 65 } })
    expect(w.findAll('[role="status"]')).toHaveLength(1)
    expect(w.get('[role="status"]').text()).toBe('直播异常')
    expect(w.get('[role="status"]').attributes('data-state')).toBe('error')
  })
})
