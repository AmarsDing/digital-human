import { describe, expect, it } from 'vitest'
import contract from '../events.contract.json'
import {
  EVENT_CONTRACT,
  FIELD_INDEX,
  FIELD_MODE,
  FIELD_TEXT,
  FIELD_UTTERANCE_ID,
  TRANSCRIPT_FINAL,
  TRANSCRIPT_PARTIAL,
  isTranscriptEvent
} from '../contract'

describe('共享事件契约（前后端一致）', () => {
  it('契约含 seq/epoch 信封字段', () => {
    expect(EVENT_CONTRACT.version).toBe(1)
    expect(contract.envelope.fields).toContain('seq')
    expect(contract.envelope.fields).toContain('epoch')
    expect(contract.envelope.fields).toContain('session_id')
  })

  it('字幕事件名与 payload 字段在契约中声明', () => {
    expect(contract.events[TRANSCRIPT_PARTIAL]).toEqual([FIELD_TEXT, FIELD_INDEX, FIELD_MODE])
    expect(contract.events[TRANSCRIPT_FINAL]).toEqual([FIELD_TEXT, FIELD_INDEX, FIELD_UTTERANCE_ID])
  })

  it('isTranscriptEvent 只认字幕事件', () => {
    expect(isTranscriptEvent(TRANSCRIPT_PARTIAL)).toBe(true)
    expect(isTranscriptEvent(TRANSCRIPT_FINAL)).toBe(true)
    expect(isTranscriptEvent('song.playing')).toBe(false)
  })
})
