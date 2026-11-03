import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

interface PanelRoute {
  method: string
  path: string
}

// vitest 以 web/ 为工作目录；jsdom 下 import.meta.url 不是 file://，故用 cwd 解析。
const routes = JSON.parse(
  readFileSync(resolve(process.cwd(), 'src/services/api/panel-routes.json'), 'utf8')
) as PanelRoute[]

const panels = readFileSync(resolve(process.cwd(), 'src/services/api/panels.ts'), 'utf8')

describe('面板路由覆盖（对齐后端 panels.go 54 条）', () => {
  it('清单固定 54 条，覆盖弹幕面板所需 RPC 之外的 HTTP 面板路由', () => {
    expect(routes).toHaveLength(54)
  })

  it('panels.ts 覆盖全部 54 条路由（含 prompt/type14 与 cost/ledger 写入）', () => {
    const missing: string[] = []
    for (const r of routes) {
      // 动态参数路由（如 /api/v1/actions/libraries/{id}）在源码里是模板字符串，按前缀匹配即可。
      const base = r.path.split('{')[0]
      if (!panels.includes(base)) missing.push(`${r.method} ${r.path}`)
    }
    expect(missing).toEqual([])
  })

  it('弹幕面板 RPC（state/pending/mode/confirm/dismiss）均已接入', () => {
    for (const path of [
      '/api/v1/danmaku/state',
      '/api/v1/danmaku/pending',
      '/api/v1/danmaku/mode',
      '/api/v1/danmaku/confirm',
      '/api/v1/danmaku/dismiss'
    ]) {
      expect(panels).toContain(path)
    }
  })
})
