import { apiPath } from './endpoint'

let bearer = ''

// lan 模式 Bearer 只存在内存（不写 localStorage/sessionStorage），刷新后需重新登录。
export function setBearer(token: string): void {
  bearer = token.trim()
}

export function currentBearer(): string {
  return bearer
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export interface RequestOptions {
  method?: string
  body?: unknown
  // multipart 上传：与 body 互斥，优先级更高。
  form?: FormData
  // blob 用于音频/导出等非 JSON 响应。
  responseType?: 'json' | 'blob'
  signal?: AbortSignal
  headers?: Record<string, string>
  timeoutMs?: number
}

const DEFAULT_TIMEOUT_MS = 10000

// 401 钩子：client 不反向依赖 pinia，由 auth store 注册，触发后设置凭证失效态。
let authErrorHandler: (() => void) | null = null
export function setAuthErrorHandler(fn: (() => void) | null): void {
  authErrorHandler = fn
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(options.headers ?? {})
  }
  let payload: BodyInit | undefined
  if (options.form) {
    payload = options.form
  } else if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(options.body)
  }
  if (bearer) headers.Authorization = `Bearer ${bearer}`

  // 默认 10s 超时；调用方自带 signal 时两者任一触发即中止。
  const timeout = AbortSignal.timeout(options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout

  let res: Response
  try {
    res = await fetch(apiPath(path), { method: options.method ?? 'GET', headers, body: payload, signal })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'TimeoutError') {
      throw new ApiError(0, 'network_timeout', '请求超时（10s），请检查网络或后端服务')
    }
    throw err
  }

  if (res.status === 401) authErrorHandler?.()

  if (options.responseType === 'blob') {
    if (!res.ok) {
      const text = await res.text()
      const error = safeParse(text) as Record<string, unknown>
      throw new ApiError(
        res.status,
        String(error.error ?? error.reason ?? error.code ?? `http_${res.status}`),
        String(error.message ?? error.error ?? `请求失败（${res.status}）`)
      )
    }
    return (await res.blob()) as T
  }

  const text = await res.text()
  const body = text ? safeParse(text) : {}
  if (!res.ok) {
    const error = body as Record<string, unknown>
    throw new ApiError(
      res.status,
      String(error.error ?? error.reason ?? error.code ?? `http_${res.status}`),
      String(error.message ?? error.error ?? `请求失败（${res.status}）`)
    )
  }
  return body as T
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return { message: text }
  }
}

// 错误归一（R-UI-36）：把后端错误码映射为可展示的中文文案。
export function classifyApiError(err: unknown): { code: string; message: string } {
  if (err instanceof ApiError) {
    switch (err.code) {
      case 'draft_revision_conflict':
      case 'draft_fencing_conflict':
      case 'draft_owner_conflict':
      case 'draft_lease_expired':
        return { code: err.code, message: '草稿已更新或编辑权限已过期，请从列表重新打开后再保存' }
      case 'unauthorized':
        return { code: err.code, message: '管理员凭证无效，请重新登录' }
      case 'network_timeout':
        return { code: err.code, message: err.message }
      case 'loopback_only':
        return { code: err.code, message: '当前为本机模式，仅允许本机访问' }
      case 'origin_not_allowed':
        return { code: err.code, message: '来源未被允许，请检查部署域名配置' }
      case 'ticket_invalid_or_used':
        return { code: err.code, message: 'WebSocket 票据已失效，正在重新连接' }
      case 'profile_changed':
        return { code: err.code, message: '预检后数字人档案或版本已变化，请前往档案页确认改动后重新预检' }
      case 'preflight_expired':
        return { code: err.code, message: '开播预检凭证已过期，请重新预检后再开播' }
      case 'preflight_required':
        return {
          code: err.code,
          message: err.message.includes('档案或版本已变化')
            ? err.message
            : '开播预检凭证缺失、未知、过期或已消费，请重新预检后再开播'
        }
      case 'draft_required':
        return { code: err.code, message: 'public-live 必须先选择并提交草稿，再携带草稿 ID 与版本重新预检；不能使用空草稿开播' }
      case 'mode_not_allowed':
        return { code: err.code, message: '服务端未允许当前开播模式，请检查后端开播配置；前端不会切换模式重试' }
      case 'publish_target_unverified':
        return { code: err.code, message: '真实抖音推流目标尚未验证，请前往平台接入完成真实推流验证后重新预检' }
      // 数字人档案（G 批 M2/M4）错误码归一。
      case 'persona_not_found':
        return { code: err.code, message: '所选数字人档案不存在或已删除，请到档案页重新选择' }
      // 保留：非死分支——live.start 失败时 StagePage personaGuide 消费该码给出「前往档案页」引导。
      case 'avatar_not_synced':
        return { code: err.code, message: '该档案形象尚未同步 Vidu，请先在档案页同步' }
      case 'avatar_expired':
        return { code: err.code, message: '形象资产已过期（Vidu 90 天），请重新同步后开播' }
      case 'voice_not_found':
        return { code: err.code, message: '音色不存在，请在档案页重新选择音色' }
      case 'vidu_key_unconfigured':
        return { code: err.code, message: 'Vidu Key 未配置，无法同步形象' }
      case 'revision_conflict':
        return { code: err.code, message: '档案已被并发修改，请刷新后重试' }
      case 'slug_conflict':
        return { code: err.code, message: 'slug 已被占用，请更换' }
      case 'avatar_in_use':
        return { code: err.code, message: '当前使用中的档案不能删除' }
      case 'too_many_refs':
        return { code: err.code, message: '参考图最多 3 张' }
      default:
        return { code: err.code, message: err.message }
    }
  }
  if (err instanceof TypeError) {
    return { code: 'network', message: '无法连接后端服务' }
  }
  return { code: 'unknown', message: err instanceof Error ? err.message : String(err) }
}
