import { apiRequest } from './client'
import type { SaveSettingsReply, SettingsReply } from './types'

export interface SettingsGroup {
  id: string
  title: string
  effective: 'immediate' | 'next_session'
  values: Record<string, unknown>
}

export function decodeSettings(reply: SettingsReply): { groups: SettingsGroup[]; revision: number } {
  return {
    revision: reply.revision,
    groups: (reply.groups ?? []).map((g) => ({
      id: g.id,
      title: g.title,
      effective: g.effective,
      values: decodeValues(g.valuesJson)
    }))
  }
}

export function encodeValues(values: Record<string, unknown>): string {
  const json = JSON.stringify(values)
  const bytes = new TextEncoder().encode(json)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}

function decodeValues(valuesJson: string): Record<string, unknown> {
  if (!valuesJson) return {}
  try {
    const bin = atob(valuesJson)
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    const text = new TextDecoder().decode(bytes)
    const parsed = JSON.parse(text)
    return typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

export async function getSettings(): Promise<{ groups: SettingsGroup[]; revision: number }> {
  const reply = await apiRequest<SettingsReply>('/api/v1/settings')
  return decodeSettings(reply)
}

export async function saveSettings(
  groups: SettingsGroup[],
  baseRevision: number
): Promise<SaveSettingsReply> {
  return apiRequest<SaveSettingsReply>('/api/v1/settings', {
    method: 'POST',
    body: {
      groups: groups.map((g) => ({
        id: g.id,
        title: g.title,
        valuesJson: encodeValues(g.values),
        effective: g.effective
      })),
      baseRevision
    }
  })
}

export async function reloadSettings(): Promise<{ ok: boolean; revision: number }> {
  return apiRequest('/api/v1/settings/reload', { method: 'POST', body: {} })
}
