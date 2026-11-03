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

  it('面板开关/位置写入 localStorage', () => {
    const p = useUiPrefs()
    p.load()
    p.togglePanel('voice')
    p.movePanel('voice', 10, 20)
    const saved = JSON.parse(localStorage.getItem('s2s.stage.panels') || '{}')
    expect(saved.voice.open).toBe(true)
    expect(saved.voice.x).toBe(10)
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
})
