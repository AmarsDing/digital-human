// 数字人档案字段级校验（v3 方案 §9.1 唯一来源，六组件共用）。
// 纯函数、无 DOM 依赖，便于 vitest 直接断言。

export const SLUG_PATTERN = /^[a-z0-9-]{1,32}$/
export const NAME_MAX = 20
export const SYSTEM_PROMPT_MAX = 2000
export const GREETING_MAX = 200
export const IMAGE_MAX_BYTES = 10 * 1024 * 1024
export const IMAGE_MAX_PIXELS = 4096
export const IMAGE_ACCEPT = 'image/png,image/jpeg'
export const IMAGE_TYPES: readonly string[] = ['image/png', 'image/jpeg']
export const REF_MAX = 3

function len(v: string): number {
  return [...v].length
}

export function validateSlug(v: string): string {
  const s = v.trim()
  if (!s) return 'slug 必填'
  if (!SLUG_PATTERN.test(s)) return 'slug 仅允许小写字母、数字、中划线，长度 1–32'
  return ''
}

export function validateName(v: string): string {
  const s = v.trim()
  if (!s) return '名称必填'
  if (len(s) > NAME_MAX) return `名称不能超过 ${NAME_MAX} 字`
  return ''
}

export function validateSystemPrompt(v: string): string {
  if (len(v) > SYSTEM_PROMPT_MAX) return `人设提示词不能超过 ${SYSTEM_PROMPT_MAX} 字`
  return ''
}

export function validateGreeting(v: string): string {
  if (len(v) > GREETING_MAX) return `开场白不能超过 ${GREETING_MAX} 字`
  return ''
}

// 上传前预检（与后端白名单一致：png/jpeg ≤10MB；svg/webp 拒绝，v3 §5.3）。
export function validateImageFile(f: { type: string; size: number }): string {
  if (!IMAGE_TYPES.includes(f.type)) return '仅支持 PNG / JPEG 图片'
  if (f.size > IMAGE_MAX_BYTES) return '图片不能超过 10MB'
  return ''
}

export function validateImagePixels(width: number, height: number): string {
  if (width > IMAGE_MAX_PIXELS || height > IMAGE_MAX_PIXELS) {
    return `图片尺寸不能超过 ${IMAGE_MAX_PIXELS}×${IMAGE_MAX_PIXELS}`
  }
  return ''
}

// 置 active 预检（v3 §9.1：形象图必填，未同步 Vidu 不允许置 active）。
export function canActivateAvatar(a: {
  avatar_asset_id?: string
  vidu_sync_state?: { state?: string }
}): string {
  if (!a.avatar_asset_id) return '请先上传形象图'
  if (a.vidu_sync_state?.state !== 'done') return '形象图未同步 Vidu，不允许设为当前'
  return ''
}
