import { defineStore } from 'pinia'
import { getSettings, reloadSettings, saveSettings, type SettingsGroup } from '../services/api/settings'
import { classifyApiError } from '../services/api/client'
import { GROUP_SPECS } from '../components/settings/schema'

function stable(values: Record<string, unknown>): string {
  return JSON.stringify(values)
}

export const useSettingsStore = defineStore('settings', {
  state: () => ({
    groups: [] as SettingsGroup[],
    revision: 0,
    baseline: {} as Record<string, string>,
    loading: false,
    saving: false,
    error: '',
    reloadRequired: [] as string[],
    savedAt: 0,
    missingGroups: [] as string[]
  }),
  getters: {
    dirtyGroupIds(state): string[] {
      return state.groups
        .filter((g) => stable(g.values) !== (state.baseline[g.id] ?? '{}'))
        .map((g) => g.id)
    },
    dirtyCount(): number {
      return this.dirtyGroupIds.length
    },
    groupById: (state) => (id: string) => state.groups.find((g) => g.id === id)
  },
  actions: {
    async load(): Promise<void> {
      this.loading = true
      this.error = ''
      try {
        const { groups, revision } = await getSettings()
        this.groups = mergeWithSchema(groups)
        this.revision = revision
        this.resetBaseline()
        this.missingGroups = GROUP_SPECS.filter((s) => !groups.some((g) => g.id === s.id)).map((s) => s.id)
      } catch (err) {
        this.error = classifyApiError(err).message
      } finally {
        this.loading = false
      }
    },

    setField(groupId: string, key: string, value: unknown): void {
      const g = this.groups.find((x) => x.id === groupId)
      if (!g) return
      g.values = { ...g.values, [key]: value }
    },

    resetBaseline(): void {
      const next: Record<string, string> = {}
      for (const g of this.groups) next[g.id] = stable(g.values)
      this.baseline = next
    },

    async save(): Promise<boolean> {
      if (!this.groups.length) return false
      this.saving = true
      this.error = ''
      try {
        const reply = await saveSettings(this.groups, this.revision)
        if (!reply.saved) {
          this.error = `保存被拒绝：${(reply.rejected ?? []).join('、') || '校验失败'}`
          return false
        }
        this.revision = reply.revision
        this.reloadRequired = reply.reloadRequired ?? []
        this.savedAt = Date.now()
        this.resetBaseline()
        return true
      } catch (err) {
        this.error = classifyApiError(err).message
        return false
      } finally {
        this.saving = false
      }
    },

    async reload(): Promise<void> {
      try {
        await reloadSettings()
      } catch (err) {
        this.error = classifyApiError(err).message
      }
      await this.load()
    }
  }
})

// 后端只存它认识的键；UI schema 负责补默认值和展示顺序，避免字段缺失导致空白。
function mergeWithSchema(groups: SettingsGroup[]): SettingsGroup[] {
  return GROUP_SPECS.map((spec) => {
    const found = groups.find((g) => g.id === spec.id)
    const values: Record<string, unknown> = { ...spec.defaults }
    if (found) {
      for (const [k, v] of Object.entries(found.values)) {
        if (v !== null && v !== undefined) values[k] = v
      }
    }
    return {
      id: spec.id,
      title: spec.title,
      effective: spec.effective,
      values
    }
  })
}
