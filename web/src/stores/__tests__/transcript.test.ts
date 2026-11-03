import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useTranscriptStore } from '../transcript'
import type { WireEvent } from '../../services/api/types'

function ev(type: string, seq: number, payload: Record<string, unknown>): WireEvent {
  return {
    seq,
    event_id: `${type}-${seq}`,
    session_id: 's1',
    type,
    ts_ms: 1000 + seq,
    epoch: 7,
    payload
  }
}

describe('transcript store 按契约消费字幕事件', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('say → partial 到达 → final 到达 → 列表落库可见', () => {
    const store = useTranscriptStore()
    store.applyEvent(ev('transcript.partial', 1, { text: '你好', index: 0, mode: 'sentence' }))
    expect(store.lines).toHaveLength(1)
    expect(store.lines[0]).toMatchObject({ text: '你好', final: false, index: 0 })

    store.applyEvent(ev('transcript.final', 2, { text: '你好，世界', index: 0, utterance_id: 'utt-1' }))
    expect(store.lines).toHaveLength(1)
    expect(store.lines[0]).toMatchObject({ text: '你好，世界', final: true, index: 0 })
  })

  it('同 index 的 partial 逐字覆盖，不堆叠重复行', () => {
    const store = useTranscriptStore()
    store.applyEvent(ev('transcript.partial', 1, { text: '你', index: 0, mode: 'sentence' }))
    store.applyEvent(ev('transcript.partial', 2, { text: '你好', index: 0, mode: 'sentence' }))
    expect(store.lines).toHaveLength(1)
    expect(store.lines[0].text).toBe('你好')

    store.applyEvent(ev('transcript.partial', 3, { text: '下一句', index: 1, mode: 'sentence' }))
    expect(store.lines).toHaveLength(2)
  })

  it('非字幕事件进入不影响字幕列表', () => {
    const store = useTranscriptStore()
    store.applyEvent(ev('song.playing', 1, { file: 'a.mp3' }))
    store.applyEvent(ev('live.started', 2, { live_id: 'lv' }))
    expect(store.lines).toHaveLength(0)
  })

  it('缺少契约字段 text 的事件被忽略', () => {
    const store = useTranscriptStore()
    store.applyEvent(ev('transcript.final', 1, { index: 0, utterance_id: 'u' }))
    expect(store.lines).toHaveLength(0)
  })
})
