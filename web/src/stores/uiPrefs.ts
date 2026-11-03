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
  // F48：统计条开关 + 拖动位置（蓝本 stage 统计条：可关、可拖、记忆）。
  stats: 's2s.stage.stats',
  statsPos: 's2s.stage.statsPos',
  // 扩展键：布局/主题偏好，全部 localStorage，不上传服务端。
  panels: 's2s.stage.panels',
  // v2 §2：单工作区激活 Tab（唯一持久化面板位）。
  workspaceTab: 's2s.stage.workspaceTab',
  theme: 's2s.theme',
  density: 's2s.density',
  colorVision: 's2s.colorvision',
  motion: 's2s.motion',
  visualQuality: 's2s.visualQuality'
}

const PANEL_ORDER: PanelId[] = ['voice', 'command', 'danmaku', 'koubo', 'refs', 'songs', 'archive']

export interface StatsPos {
  x: number
  y: number
}

/** 统计条默认位 = {0,0}（停靠页面底部通栏）；拖动后为浮动坐标。 */
export const DEFAULT_STATS_POS: StatsPos = { x: 0, y: 0 }

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
    stats: true,
    statsPos: { ...DEFAULT_STATS_POS } as StatsPos,
    panels: JSON.parse(JSON.stringify(DEFAULT_PANELS)) as Record<PanelId, PanelLayout>,
    activeWorkspaceTab: 'danmaku' as PanelId,
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
      this.stats = asBool(readRaw(KEY.stats, 'on'), true)
      this.statsPos = asStatsPos(read<StatsPos>(KEY.statsPos, DEFAULT_STATS_POS))
      // 旧 open/x/y/collapsed 只迁移一次（v2 §2.5）：读取唯一打开的 panel 作为 activeTab，
      // 忽略旧几何并清理旧键；不再恢复成多个同时打开的面板。
      const legacyRaw = localStorage.getItem(KEY.panels)
      if (legacyRaw !== null) {
        try {
          const legacy = JSON.parse(legacyRaw) as Partial<Record<PanelId, Partial<PanelLayout>>>
          const opened = PANEL_ORDER.filter((id) => legacy?.[id]?.open)
          this.activeWorkspaceTab = read<PanelId>(KEY.workspaceTab, opened[0] ?? 'danmaku')
        } catch {
          this.activeWorkspaceTab = 'danmaku'
        }
        localStorage.removeItem(KEY.panels)
        write(KEY.workspaceTab, this.activeWorkspaceTab)
        this.panels = JSON.parse(JSON.stringify(DEFAULT_PANELS))
      } else {
        this.activeWorkspaceTab = read<PanelId>(KEY.workspaceTab, 'danmaku')
      }
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
    // F48：统计条开关
    setStats(on: boolean): void {
      this.stats = on
      writeRaw(KEY.stats, on ? 'on' : 'off')
    },
    // F48：统计条拖动位置记忆；{0,0} = 复位回底部通栏
    setStatsPos(x: number, y: number): void {
      this.statsPos = asStatsPos({ x, y })
      write(KEY.statsPos, this.statsPos)
    },
    resetStatsPos(): void {
      this.setStatsPos(0, 0)
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
      // v2 §2：多面板浮层几何不再持久化，只保留内存态；激活 Tab 由 setActiveWorkspaceTab 持久。
    },
    setActiveWorkspaceTab(tab: PanelId): void {
      this.activeWorkspaceTab = tab
      write(KEY.workspaceTab, tab)
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

function asStatsPos(raw: StatsPos): StatsPos {
  const x = Number(raw?.x)
  const y = Number(raw?.y)
  return {
    x: Number.isFinite(x) ? Math.max(0, Math.round(x)) : 0,
    y: Number.isFinite(y) ? Math.max(0, Math.round(y)) : 0
  }
}
