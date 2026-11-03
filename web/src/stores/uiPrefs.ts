import { defineStore } from 'pinia'

export type PanelId = 'voice' | 'command' | 'danmaku' | 'koubo' | 'refs' | 'songs' | 'archive'
export type ThemeMode = 'dark' | 'light'
export type DensityMode = 'normal' | 'compact'
export type MotionMode = 'full' | 'reduced'
export type VisualQuality = 'full' | 'eco'

export interface PanelLayout {
  open: boolean
  collapsed: boolean
  x: number
  y: number
}

const KEY = {
  cmdFx: 's2s.stage.cmdFx',
  rightW: 's2s.stage.rightW',
  kbCmdOpen: 's2s.stage.kbCmdOpen',
  // 扩展键：布局/主题偏好，全部 localStorage，不上传服务端。
  panels: 's2s.stage.panels',
  theme: 's2s.theme',
  density: 's2s.density',
  colorVision: 's2s.colorvision',
  motion: 's2s.motion',
  visualQuality: 's2s.visualQuality'
}

export const DEFAULT_PANELS: Record<PanelId, PanelLayout> = {
  voice: { open: false, collapsed: false, x: 24, y: 24 },
  command: { open: false, collapsed: false, x: 24, y: 340 },
  danmaku: { open: false, collapsed: false, x: 372, y: 340 },
  koubo: { open: false, collapsed: false, x: 372, y: 24 },
  refs: { open: false, collapsed: false, x: 720, y: 340 },
  songs: { open: false, collapsed: false, x: 720, y: 24 },
  archive: { open: false, collapsed: false, x: 1068, y: 24 }
}

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

function readRaw(key: string, fallback: string): string {
  return localStorage.getItem(key) ?? fallback
}

function write(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // 存储满/隐私模式：不阻断交互。
  }
}

function writeRaw(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // ignore
  }
}

function asBool(raw: string, fallback: boolean): boolean {
  if (raw === 'on' || raw === 'true') return true
  if (raw === 'off' || raw === 'false') return false
  return fallback
}

export const useUiPrefs = defineStore('uiPrefs', {
  state: () => ({
    cmdFx: true,
    rightW: 360,
    kbCmdOpen: false,
    panels: JSON.parse(JSON.stringify(DEFAULT_PANELS)) as Record<PanelId, PanelLayout>,
    theme: 'dark' as ThemeMode,
    density: 'normal' as DensityMode,
    colorVision: 'base' as 'base' | 'safe',
    motion: 'full' as MotionMode,
    visualQuality: 'full' as VisualQuality
  }),
  actions: {
    load(): void {
      this.cmdFx = asBool(readRaw(KEY.cmdFx, 'on'), true)
      this.rightW = clampWidth(Number(read<number>(KEY.rightW, 360)))
      this.kbCmdOpen = read<boolean>(KEY.kbCmdOpen, false)
      this.panels = { ...JSON.parse(JSON.stringify(DEFAULT_PANELS)), ...read(KEY.panels, {}) }
      this.theme = read<ThemeMode>(KEY.theme, 'dark')
      this.density = read<DensityMode>(KEY.density, 'normal')
      this.colorVision = read(KEY.colorVision, 'base')
      this.motion = read<MotionMode>(KEY.motion, 'full')
      this.visualQuality = read<VisualQuality>(KEY.visualQuality, 'full')
      this.applyDom()
    },

    setCmdFx(on: boolean): void {
      this.cmdFx = on
      writeRaw(KEY.cmdFx, on ? 'on' : 'off')
    },
    setRightW(width: number): void {
      this.rightW = clampWidth(width)
      write(KEY.rightW, this.rightW)
    },
    setKbCmdOpen(open: boolean): void {
      this.kbCmdOpen = open
      write(KEY.kbCmdOpen, open)
    },
    togglePanel(id: PanelId): void {
      this.panels[id].open = !this.panels[id].open
      this.savePanels()
    },
    movePanel(id: PanelId, x: number, y: number): void {
      this.panels[id].x = Math.max(0, x)
      this.panels[id].y = Math.max(0, y)
      this.savePanels()
    },
    toggleCollapse(id: PanelId): void {
      this.panels[id].collapsed = !this.panels[id].collapsed
      this.savePanels()
    },
    savePanels(): void {
      write(KEY.panels, this.panels)
    },
    setTheme(v: ThemeMode): void {
      this.theme = v
      write(KEY.theme, v)
      this.applyDom()
    },
    setDensity(v: DensityMode): void {
      this.density = v
      write(KEY.density, v)
      this.applyDom()
    },
    setColorVision(v: 'base' | 'safe'): void {
      this.colorVision = v
      write(KEY.colorVision, v)
      this.applyDom()
    },
    setMotion(v: MotionMode): void {
      this.motion = v
      write(KEY.motion, v)
      this.applyDom()
    },
    setVisualQuality(v: VisualQuality): void {
      this.visualQuality = v
      write(KEY.visualQuality, v)
      this.applyDom()
    },
    applyDom(): void {
      if (typeof document === 'undefined') return
      const el = document.documentElement
      el.dataset.theme = this.theme
      el.dataset.density = this.density
      el.dataset.colorvision = this.colorVision
      el.dataset.motion = this.motion
      el.dataset.visualQuality = this.visualQuality
    }
  }
})

function clampWidth(v: number): number {
  if (!Number.isFinite(v)) return 360
  return Math.min(560, Math.max(280, Math.round(v)))
}
