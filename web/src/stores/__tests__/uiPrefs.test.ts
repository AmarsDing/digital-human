import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useUiPrefs } from '../uiPrefs'

describe('uiPrefs', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('默认值符合本机偏好约定', () => {
    const p = useUiPrefs()
    p.load()
    expect(p.cmdFx).toBe(true)
    expect(p.rightW).toBe(360)
    expect(p.kbCmdOpen).toBe(false)
    expect(p.panels.voice.open).toBe(false)
  })

  it('右侧栏宽度夹在 280–560 之间并持久化', () => {
    const p = useUiPrefs()
    p.load()
    p.setRightW(100)
    expect(p.rightW).toBe(280)
    p.setRightW(900)
    expect(p.rightW).toBe(560)
    expect(localStorage.getItem('s2s.stage.rightW')).toBe('560')
  })

  it('面板开关/位置只保留内存态，不再持久化旧 panels 键（v2 §2）', () => {
    const p = useUiPrefs()
    p.load()
    p.togglePanel('voice')
    p.movePanel('voice', 10, 20)
    expect(p.panels.voice.open).toBe(true)
    expect(p.panels.voice.x).toBe(10)
    expect(localStorage.getItem('s2s.stage.panels')).toBeNull()
  })

  it('activeWorkspaceTab 持久化（v2 §2 单工作区）', () => {
    const p = useUiPrefs()
    p.load()
    expect(p.activeWorkspaceTab).toBe('danmaku')
    p.setActiveWorkspaceTab('koubo')
    expect(localStorage.getItem('s2s.stage.workspaceTab')).toBe('"koubo"')
    const q = useUiPrefs()
    q.load()
    expect(q.activeWorkspaceTab).toBe('koubo')
  })

  it('旧 panels 键只迁移一次：取唯一打开的 panel 为 activeTab，忽略几何并清理旧键', () => {
    localStorage.setItem(
      's2s.stage.panels',
      JSON.stringify({ koubo: { open: true, collapsed: true, x: 500, y: 400 }, danmaku: { open: false, collapsed: false, x: 1, y: 2 } })
    )
    const p = useUiPrefs()
    p.load()
    expect(p.activeWorkspaceTab).toBe('koubo')
    expect(localStorage.getItem('s2s.stage.panels')).toBeNull()
    expect(localStorage.getItem('s2s.stage.workspaceTab')).toBe('"koubo"')
    // 面板复位默认，不恢复多个同时打开
    expect(p.panels.koubo.open).toBe(false)
    // 迁移只发生一次：再次 load 不再改写 activeTab
    p.setActiveWorkspaceTab('refs')
    const q = useUiPrefs()
    q.load()
    expect(q.activeWorkspaceTab).toBe('refs')
  })

  it('旧 panels 键脏数据：回退默认 danmaku 并清理旧键', () => {
    localStorage.setItem('s2s.stage.panels', 'not-json')
    const p = useUiPrefs()
    p.load()
    expect(p.activeWorkspaceTab).toBe('danmaku')
    expect(localStorage.getItem('s2s.stage.panels')).toBeNull()
  })

  it('主题/密度/色觉写入 html data-*', () => {
    const p = useUiPrefs()
    p.load()
    p.setTheme('light')
    p.setDensity('compact')
    p.setColorVision('safe')
    p.setMotion('reduced')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(document.documentElement.dataset.density).toBe('compact')
    expect(document.documentElement.dataset.colorvision).toBe('safe')
    expect(document.documentElement.dataset.motion).toBe('reduced')
  })

  it('F48：统计条开关默认开，关闭后持久化 s2s.stage.stats', () => {
    const p = useUiPrefs()
    p.load()
    expect(p.stats).toBe(true)
    p.setStats(false)
    expect(localStorage.getItem('s2s.stage.stats')).toBe('off')
    const q = useUiPrefs()
    q.load()
    expect(q.stats).toBe(false)
  })

  it('F48：统计条位置记忆（夹取负数、取整），复位回 {0,0}', () => {
    const p = useUiPrefs()
    p.load()
    expect(p.statsPos).toEqual({ x: 0, y: 0 })
    p.setStatsPos(-5, 12.6)
    expect(p.statsPos).toEqual({ x: 0, y: 13 })
    expect(JSON.parse(localStorage.getItem('s2s.stage.statsPos') || '{}')).toEqual({ x: 0, y: 13 })
    p.resetStatsPos()
    expect(p.statsPos).toEqual({ x: 0, y: 0 })
  })

  it('F48：statsPos 脏数据回退默认位', () => {
    localStorage.setItem('s2s.stage.statsPos', '{"x":"bad"}')
    const p = useUiPrefs()
    p.load()
    expect(p.statsPos).toEqual({ x: 0, y: 0 })
  })
})
