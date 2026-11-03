// 与后端 pkg/eventcontract 共享的事件契约（唯一事实来源：events.contract.json）。
import contract from './events.contract.json'

export const EVENT_CONTRACT = contract

export const TRANSCRIPT_PARTIAL = 'transcript.partial'
export const TRANSCRIPT_FINAL = 'transcript.final'

// payload 字段名，避免两侧手写字符串漂移。
export const FIELD_TEXT = 'text'
export const FIELD_INDEX = 'index'
export const FIELD_MODE = 'mode'
export const FIELD_UTTERANCE_ID = 'utterance_id'

export function isTranscriptEvent(type: string): boolean {
  return type === TRANSCRIPT_PARTIAL || type === TRANSCRIPT_FINAL
}
