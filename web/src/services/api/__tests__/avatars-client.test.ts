import { describe, expect, it } from 'vitest'
import { ApiError, classifyApiError } from '../client'
import { avatarImageUrl, avatarRefImageUrl } from '../panels'

// avatars 客户端 URL 形状 + G 批错误码归一（M4）。
describe('avatars 客户端', () => {
  it('图片 URL 形状（fetch→blob 使用）', () => {
    expect(avatarImageUrl('abc')).toBe('/api/v1/avatars/abc/image')
    expect(avatarRefImageUrl('abc', 'r1')).toBe('/api/v1/avatars/abc/assets/r1/image')
    expect(avatarImageUrl('a b')).toBe('/api/v1/avatars/a%20b/image')
  })

  it('G 批错误码归一为可读文案', () => {
    const cases: Array<[string, string]> = [
      ['persona_not_found', '档案'],
      ['avatar_not_synced', '同步'],
      ['avatar_expired', '过期'],
      ['voice_not_found', '音色'],
      ['vidu_key_unconfigured', 'Vidu Key'],
      ['revision_conflict', '并发'],
      ['slug_conflict', 'slug'],
      ['avatar_in_use', '删除'],
      ['too_many_refs', '3']
    ]
    for (const [code, keyword] of cases) {
      const r = classifyApiError(new ApiError(400, code, 'raw'))
      expect(r.code).toBe(code)
      expect(r.message).toContain(keyword)
      expect(r.message).not.toBe('raw')
    }
  })

  it('未知 code 透传后端 message', () => {
    const r = classifyApiError(new ApiError(500, 'unknown_x', 'backend said'))
    expect(r.code).toBe('unknown_x')
    expect(r.message).toBe('backend said')
  })
})
