import { defineStore } from 'pinia'
import {
  activateAvatar,
  createAvatar,
  deleteAvatar,
  duplicateAvatar,
  getAvatar,
  listAvatars,
  syncAvatarToVidu,
  updateAvatar,
  type AvatarDetail,
  type AvatarListItem,
  type AvatarUpsertInput,
  type ViduSyncState
} from '../services/api/panels'
import { ApiError, classifyApiError } from '../services/api/client'

// sync 轮询参数（v3 §2.2）：间隔 2s，总超时 480s，超时转错误态可手动重试。
export const SYNC_POLL_MS = 2000
export const SYNC_TIMEOUT_MS = 480000

interface AvatarsState {
  items: AvatarListItem[]
  total: number
  storage: string
  loading: boolean
  error: string
  syncing: Record<string, boolean>
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    function onAbort(): void {
      clearTimeout(t)
      reject(new DOMException('aborted', 'AbortError'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

export function syncTerminal(state: ViduSyncState | undefined): boolean {
  return state?.state === 'done' || state?.state === 'error'
}

export const useAvatarsStore = defineStore('avatars', {
  state: (): AvatarsState => ({
    items: [],
    total: 0,
    storage: '',
    loading: false,
    error: '',
    syncing: {}
  }),
  getters: {
    activeAvatar: (s) => s.items.find((i) => i.is_active) ?? null
  },
  actions: {
    async load(signal?: AbortSignal): Promise<void> {
      this.loading = true
      this.error = ''
      try {
        const r = await listAvatars(1, 100, false, signal)
        this.items = r.avatars ?? []
        this.total = r.total ?? this.items.length
        this.storage = r.storage ?? ''
      } catch (err) {
        this.error = classifyApiError(err).message
      } finally {
        this.loading = false
      }
    },

    async detail(id: string, signal?: AbortSignal): Promise<AvatarDetail | null> {
      try {
        return await getAvatar(id, signal)
      } catch (err) {
        this.error = classifyApiError(err).message
        return null
      }
    },

    async create(input: AvatarUpsertInput): Promise<AvatarDetail> {
      return createAvatar(input)
    },

    async update(id: string, input: AvatarUpsertInput, revision: number): Promise<AvatarDetail> {
      return updateAvatar(id, input, revision)
    },

    async remove(id: string): Promise<boolean> {
      try {
        await deleteAvatar(id)
        await this.load()
        return true
      } catch (err) {
        this.error = classifyApiError(err).message
        return false
      }
    },

    async duplicate(id: string): Promise<AvatarDetail | null> {
      try {
        const a = await duplicateAvatar(id)
        await this.load()
        return a
      } catch (err) {
        this.error = classifyApiError(err).message
        return null
      }
    },

    async activate(id: string): Promise<boolean> {
      try {
        await activateAvatar(id)
        await this.load()
        return true
      } catch (err) {
        this.error = classifyApiError(err).message
        return false
      }
    },

    // sync-vidu 202 异步闭环：提交后轮询 GetAvatar 的 vidu_sync_state 直到终态/超时。
    // 按钮 loading 期间 syncing[id]=true 防连点；总超时 480s 抛错由 UI 转错误态。
    async syncAndWait(id: string, signal?: AbortSignal): Promise<ViduSyncState> {
      if (this.syncing[id]) {
        throw new ApiError(0, 'sync_in_flight', '同步进行中，请勿重复提交')
      }
      this.syncing = { ...this.syncing, [id]: true }
      const startedAt = Date.now()
      try {
        await syncAvatarToVidu(id)
        for (;;) {
          await sleep(SYNC_POLL_MS, signal)
          const d = await getAvatar(id, signal)
          const st = d.vidu_sync_state
          if (syncTerminal(st)) return st
          if (Date.now() - startedAt > SYNC_TIMEOUT_MS) {
            throw new ApiError(0, 'sync_timeout', '同步超时（480s），请稍后查看状态或手动重试')
          }
        }
      } finally {
        const rest = { ...this.syncing }
        delete rest[id]
        this.syncing = rest
        void this.load()
      }
    }
  }
})
