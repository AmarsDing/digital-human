import { defineStore } from 'pinia'
import type { WireEvent } from '../services/api/types'
import {
  FIELD_INDEX,
  FIELD_TEXT,
  FIELD_UTTERANCE_ID,
  TRANSCRIPT_FINAL,
  isTranscriptEvent
} from '../services/api/contract'

export const TRANSCRIPT_CAP = 200

export interface TranscriptLine {
  id: string
  tsMs: number
  text: string
  final: boolean
  index: number
  utteranceId: string
}

interface TranscriptState {
  lines: TranscriptLine[]
  cleared: number
}

// 按契约取 payload.text；index 缺省时退回事件 seq，保证列表顺序稳定。
function textOf(e: WireEvent): string {
  const p = e.payload ?? {}
  return String(p[FIELD_TEXT] ?? '').trim()
}

function indexOf(e: WireEvent): number {
  const raw = (e.payload ?? {})[FIELD_INDEX]
  const n = typeof raw === 'number' ? raw : Number(raw)
  return Number.isFinite(n) ? n : e.seq
}

function utteranceOf(e: WireEvent): string {
  return String((e.payload ?? {})[FIELD_UTTERANCE_ID] ?? '')
}

// partial/final 归并键：双方都带 utterance_id 时按 id 归并（交错发言不串行）；
// 任一方缺 id 时退回 index（同一说话位置的 partial→final 原地转正）。
function sameUtterance(l: TranscriptLine, index: number, utteranceId: string): boolean {
  if (utteranceId && l.utteranceId) return l.utteranceId === utteranceId
  return l.index === index
}

export const useTranscriptStore = defineStore('transcript', {
  state: (): TranscriptState => ({ lines: [], cleared: 0 }),
  actions: {
    seed(lines: string[]): void {
      if (!lines?.length) return
      this.lines = lines.map((text, i) => ({
        id: `seed-${i}`,
        tsMs: 0,
        text,
        final: true,
        index: i,
        utteranceId: ''
      }))
    },

    applyEvent(e: WireEvent): void {
      if (!isTranscriptEvent(e.type)) return
      const text = textOf(e)
      if (!text) return
      const final = e.type === TRANSCRIPT_FINAL
      const index = indexOf(e)
      const utteranceId = utteranceOf(e)
      if (!final) {
        // partial 覆盖同一 utterance（退回 index）的行，避免逐字重复堆叠。
        const row = this.lines.find((l) => !l.final && sameUtterance(l, index, utteranceId))
        if (row) {
          row.text = text
          row.tsMs = e.ts_ms
          return
        }
        this.lines.push({ id: e.event_id, tsMs: e.ts_ms, text, final, index, utteranceId })
        this.trim()
        return
      }
      // final 与 partial 同 utterance：原地转正，不重复追加。
      const partial = this.lines.find((l) => !l.final && sameUtterance(l, index, utteranceId))
      if (partial) {
        partial.text = text
        partial.final = true
        partial.id = e.event_id || partial.id
        partial.tsMs = e.ts_ms
        return
      }
      this.lines.push({ id: e.event_id, tsMs: e.ts_ms, text, final, index, utteranceId })
      this.trim()
    },

    trim(): void {
      if (this.lines.length > TRANSCRIPT_CAP) {
        this.lines.splice(0, this.lines.length - TRANSCRIPT_CAP)
      }
    },

    clear(): void {
      this.cleared += this.lines.length
      this.lines = []
    }
  }
})
