// objectURL 预算池（v3 方案 §9.2）：同屏 blob ≤8 个；revoke 四时机由调用方触发
// （组件卸载 / 换图 / 弹层关闭 / 列表刷新前），本模块只负责计数、驱逐与回收。
// 驱逐策略：预算满时回收最久未用（LRU）项并通过 onEvict 回调通知宿主回退占位符。

export const BLOB_BUDGET = 8

export interface BlobEntry {
  key: string
  url: string
  lastUsed: number
  onEvict?: () => void
}

const entries = new Map<string, BlobEntry>()

// 单调计数器代替 Date.now()：避免同 tick 注册时间戳并列导致 LRU 不稳定。
let clock = 0
function nextTick_(): number {
  clock += 1
  return clock
}

export function blobCount(): number {
  return entries.size
}

export function canAllocateBlob(): boolean {
  return entries.size < BLOB_BUDGET
}

function drop(key: string): void {
  const e = entries.get(key)
  if (!e) return
  entries.delete(key)
  URL.revokeObjectURL(e.url)
  e.onEvict?.()
}

// 注册一个已创建的 objectURL。预算满时驱逐 LRU；同 key 先释放旧值（换图时机）。
export function registerBlob(key: string, url: string, onEvict?: () => void): void {
  if (entries.has(key)) drop(key)
  if (entries.size >= BLOB_BUDGET) {
    let lru: BlobEntry | null = null
    for (const e of entries.values()) {
      if (!lru || e.lastUsed < lru.lastUsed) lru = e
    }
    if (lru) drop(lru.key)
  }
  entries.set(key, { key, url, lastUsed: nextTick_(), onEvict })
}

export function touchBlob(key: string): void {
  const e = entries.get(key)
  if (e) e.lastUsed = nextTick_()
}

// 主动释放（组件卸载 / 弹层关闭 / 列表刷新前）。
export function releaseBlob(key: string): void {
  drop(key)
}

// 列表刷新前批量回收：默认全清，可按前缀只清某一类（如 "avatar-list:"）。
export function releaseBlobs(keyPrefix?: string): void {
  for (const key of [...entries.keys()]) {
    if (!keyPrefix || key.startsWith(keyPrefix)) drop(key)
  }
}

// 测试用：重置池（不触发 onEvict）。
export function resetBlobPool(): void {
  for (const key of [...entries.keys()]) {
    const e = entries.get(key)
    entries.delete(key)
    if (e) URL.revokeObjectURL(e.url)
  }
}
