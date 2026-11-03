import { describe, expect, it } from 'vitest'
import { emptyBuffer, mergeEvent } from '../events-core'
import type { WireEvent } from '../../services/api/types'

function ev(seq: number, overrides: Partial<WireEvent> = {}): WireEvent {
  return {
    seq,
    event_id: `e-${seq}`,
    session_id: 's1',
    type: 'live.tick',
    ts_ms: 1000 + seq,
    epoch: 1,
    ...overrides
  }
}

describe('events-core', () => {
  it('按 seq 追加并去重', () => {
    let buffer = emptyBuffer()
    const a = mergeEvent(buffer, ev(1))
    buffer = a.buffer
    expect(buffer.events).toHaveLength(1)
    expect(buffer.lastSeq).toBe(1)

    const dup = mergeEvent(buffer, ev(1))
    expect(dup.duplicate).toBe(true)
    expect(dup.buffer.events).toHaveLength(1)
  })

  it('检测 seq 跳跃并要求 resync', () => {
    let buffer = mergeEvent(emptyBuffer(), ev(1)).buffer
    buffer = mergeEvent(buffer, ev(2)).buffer
    const jump = mergeEvent(buffer, ev(5))
    expect(jump.gap).toBe(true)
    expect(jump.buffer.lastSeq).toBe(5)
  })

  it('同场次内切歌/换参考图/动作触发不清空列表，去重仍有效', () => {
    let buffer = mergeEvent(emptyBuffer(), ev(1, { epoch: 7 })).buffer
    buffer = mergeEvent(buffer, ev(2, { epoch: 7, type: 'song.playing' })).buffer
    buffer = mergeEvent(buffer, ev(3, { epoch: 7, type: 'refs.mounted' })).buffer
    buffer = mergeEvent(buffer, ev(4, { epoch: 7, type: 'action.sent' })).buffer
    expect(buffer.events.map((e) => e.seq)).toEqual([1, 2, 3, 4])
    expect(buffer.epoch).toBe(7)

    // 同一事件再次投递：去重表仍在，不重复入列。
    const dup = mergeEvent(buffer, ev(4, { epoch: 7, type: 'action.sent' }))
    expect(dup.duplicate).toBe(true)
    expect(dup.buffer.events).toHaveLength(4)
  })

  it('epoch 意外漂移只记录不清空（清空由换场次负责）', () => {
    let buffer = mergeEvent(emptyBuffer(), ev(1, { epoch: 7 })).buffer
    buffer = mergeEvent(buffer, ev(2, { epoch: 7 })).buffer
    const drift = mergeEvent(buffer, ev(3, { epoch: 8 }))
    expect(drift.epochChanged).toBe(true)
    expect(drift.buffer.events.map((e) => e.seq)).toEqual([1, 2, 3])
    expect(drift.buffer.epoch).toBe(8)
  })

  it('超出上限裁剪最旧事件；乱序/回放被 seq 水位拒绝', () => {
    let buffer = emptyBuffer()
    for (let i = 1; i <= 5; i++) buffer = mergeEvent(buffer, ev(i), 3).buffer
    expect(buffer.events.map((e) => e.seq)).toEqual([3, 4, 5])
    expect(buffer.dropped).toBe(2)
    // seq <= lastSeq（5）一律视为乱序/回放，即使 event_id 已被容量淘汰。
    const replay = mergeEvent(buffer, ev(1), 3)
    expect(replay.duplicate).toBe(true)
    expect(replay.buffer.events.map((e) => e.seq)).toEqual([3, 4, 5])
    expect(replay.buffer.lastSeq).toBe(5)
  })

  it('畸形事件（缺 seq/type）计 droppedMalformed 且不入列', () => {
    const buffer = emptyBuffer()
    const bad = mergeEvent(buffer, { ...ev(1), seq: Number.NaN })
    expect(bad.duplicate).toBe(true)
    expect(bad.buffer.droppedMalformed).toBe(1)
    expect(bad.buffer.events).toHaveLength(0)
    const noType = mergeEvent(bad.buffer, { ...ev(2), type: '' })
    expect(noType.buffer.droppedMalformed).toBe(2)
    expect(noType.buffer.events).toHaveLength(0)
  })

  it('乱序到达不回退水位', () => {
    let buffer = mergeEvent(emptyBuffer(), ev(1)).buffer
    buffer = mergeEvent(buffer, ev(3)).buffer // gap
    expect(buffer.lastSeq).toBe(3)
    const late = mergeEvent(buffer, ev(2))
    expect(late.duplicate).toBe(true)
    expect(late.buffer.lastSeq).toBe(3)
    expect(late.buffer.events.map((e) => e.seq)).toEqual([1, 3])
  })

  it('保留 request_id 供事件面板展示关联链路', () => {
    const out = mergeEvent(emptyBuffer(), ev(1, { request_id: 'req-abc' }))
    expect(out.buffer.events[0].request_id).toBe('req-abc')
  })
})
