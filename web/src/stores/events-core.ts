import type { WireEvent } from '../services/api/types'

export const EVENT_CAP = 300

export interface EventBuffer {
  events: WireEvent[]
  seen: Record<string, true>
  lastSeq: number
  epoch: number
  dropped: number
  droppedMalformed: number
}

export function emptyBuffer(): EventBuffer {
  return { events: [], seen: {}, lastSeq: 0, epoch: 0, dropped: 0, droppedMalformed: 0 }
}

export interface MergeOutcome {
  buffer: EventBuffer
  duplicate: boolean
  gap: boolean
  epochChanged: boolean
}

// 事件合并是纯函数：event_id 去重、seq 水位拒绝乱序/回放、seq 跳跃标记 resync。
// epoch 只做记录：同一场次内 epoch 恒定，切歌/换参考图/动作触发都不清空列表；
// 清空与去重表重置只在场次切换时由 switchSession 显式触发。
export function mergeEvent(buffer: EventBuffer, incoming: WireEvent, cap = EVENT_CAP): MergeOutcome {
  // 畸形事件（缺 seq/type）不入场，计入 droppedMalformed 供面板透出。
  if (!incoming || typeof incoming.seq !== 'number' || !Number.isFinite(incoming.seq) || !incoming.type) {
    return {
      buffer: { ...buffer, droppedMalformed: buffer.droppedMalformed + 1 },
      duplicate: true,
      gap: false,
      epochChanged: false
    }
  }
  const id = incoming.event_id || `seq:${incoming.seq}`
  if (buffer.seen[id]) {
    return { buffer, duplicate: true, gap: false, epochChanged: false }
  }
  // seq 水位：<= lastSeq 一律视为乱序/重复回放，拒绝且不回退水位。
  if (buffer.lastSeq > 0 && incoming.seq <= buffer.lastSeq) {
    return { buffer, duplicate: true, gap: false, epochChanged: false }
  }
  // 同场次内 seq 从 1 递增；lastSeq 更大说明是真的跳号，需要补拉。
  const gap = buffer.lastSeq > 0 && incoming.seq > buffer.lastSeq + 1

  const events = [...buffer.events]
  const seen = { ...buffer.seen }
  const epochChanged = buffer.lastSeq > 0 && incoming.epoch !== buffer.epoch

  events.push(incoming)
  seen[id] = true
  let dropped = buffer.dropped
  if (events.length > cap) {
    const removed = events.splice(0, events.length - cap)
    dropped += removed.length
    // seen 只配合 event_id 去重，容量淘汰不再删除 seen（乱序由 seq 水位兜底）。
  }

  return {
    buffer: {
      events,
      seen,
      lastSeq: Math.max(buffer.lastSeq, incoming.seq),
      epoch: incoming.epoch,
      dropped,
      droppedMalformed: buffer.droppedMalformed
    },
    duplicate: false,
    gap,
    epochChanged
  }
}
