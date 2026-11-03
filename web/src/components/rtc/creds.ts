import type { RtcToken } from '../../services/api/types'
import type { RtcCreds } from './types'

// 提前刷新的余量：到期前 60s 主动换新 token，避免入会中被踢。
export const REFRESH_LEAD_MS = 60_000

// 后端未配置时返回 ok=false；此时必须保持 null，让 UI 明确显示"未配置"。
export function toCreds(reply: RtcToken): RtcCreds | null {
  if (!reply.ok || !reply.token || !reply.roomId || !reply.userId) return null
  return {
    mode: reply.mode === 'customer' ? 'customer' : 'managed',
    provider: reply.provider === 'trtc' ? 'trtc' : 'artc',
    appId: '',
    roomId: reply.roomId,
    userId: reply.userId,
    token: reply.token,
    tokenExpireAt: reply.expiresAt
  }
}

// 刷新时机：到期前 lead 秒；TTL 很短时取剩余时间一半，避免 1s 抖动重取。
export function refreshDelay(expiresAt: number, now = Date.now(), leadMs = REFRESH_LEAD_MS): number {
  if (!expiresAt) return -1
  const left = expiresAt - now
  if (left <= 0) return 0
  const delay = left > leadMs * 2 ? left - leadMs : left / 2
  return Math.max(1000, Math.floor(delay))
}

// 刷新失败降级：保留旧凭据继续用到期，只把错误透出给 UI；无旧凭据则回到未配置。
export function degradeOnRefreshError(prev: RtcCreds | null, reason: string): { creds: RtcCreds | null; error: string } {
  return { creds: prev, error: reason }
}
