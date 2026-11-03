// 枚举裸渲治理：phase / 事件 type 的中文映射唯一来源，原码放 title 保留排查能力。
export const PHASE_MAP: Record<string, string> = {
  idle: '未开播',
  starting: '连线中',
  live: '直播中',
  stopping: '停止中',
  ended: '已结束',
  error: '异常'
}

export function phaseLabel(phase: string): string {
  return PHASE_MAP[phase] ?? phase
}

const EVENT_TYPE_MAP: Record<string, string> = {
  'live.start_requested': '请求开播',
  'live.started': '已开播',
  'live.start_failed': '开播失败',
  'live.stopped': '已停止',
  'live.crash_recovered': '崩溃恢复',
  'transcript.partial': '字幕（部分）',
  'transcript.final': '字幕',
  'song.playing': '切歌',
  'refs.mounted': '参考图挂载',
  'action.sent': '动作触发'
}

export function eventTypeLabel(type: string): string {
  return EVENT_TYPE_MAP[type] ?? type
}
