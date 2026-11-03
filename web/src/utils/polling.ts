// 页面可见性轮询：隐藏时暂停计时，回到前台立即补一次执行。
export interface VisibilityPolling {
  start(): void
  stop(): void
  tick(): void
  running(): boolean
}

export function createVisibilityPolling(fn: () => void, intervalMs: number): VisibilityPolling {
  let timer: ReturnType<typeof setInterval> | null = null

  function onVisible(): void {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') fn()
  }

  function start(): void {
    if (timer) return
    timer = setInterval(() => {
      if (typeof document === 'undefined' || document.visibilityState !== 'hidden') fn()
    }, intervalMs)
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible)
  }

  function stop(): void {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible)
  }

  return {
    start,
    stop,
    tick: fn,
    running: () => timer !== null
  }
}
