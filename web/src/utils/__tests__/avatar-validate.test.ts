import { describe, expect, it } from 'vitest'
import {
  IMAGE_MAX_BYTES,
  canActivateAvatar,
  validateGreeting,
  validateImageFile,
  validateImagePixels,
  validateName,
  validateSlug,
  validateSystemPrompt
} from '../avatar-validate'

// v3 方案 §9.1 字段级校验表逐条落点。
describe('avatar-validate', () => {
  describe('slug ^[a-z0-9-]{1,32}$', () => {
    it('接受合法值', () => {
      expect(validateSlug('xiao-ya')).toBe('')
      expect(validateSlug('a')).toBe('')
      expect(validateSlug('a'.repeat(32))).toBe('')
    })
    it('拒绝空值', () => {
      expect(validateSlug('')).toContain('必填')
      expect(validateSlug('   ')).toContain('必填')
    })
    it('拒绝大写/下划线/中文/超长', () => {
      expect(validateSlug('Xiaoya')).toContain('小写')
      expect(validateSlug('xiao_ya')).toContain('小写')
      expect(validateSlug('小雅')).toContain('小写')
      expect(validateSlug('a'.repeat(33))).toContain('小写')
    })
  })

  describe('name 1–20', () => {
    it('边界', () => {
      expect(validateName('小雅')).toBe('')
      expect(validateName('a'.repeat(20))).toBe('')
      expect(validateName('a'.repeat(21))).toContain('20')
      expect(validateName('')).toContain('必填')
    })
  })

  describe('system_prompt ≤2000 / greeting ≤200', () => {
    it('边界', () => {
      expect(validateSystemPrompt('a'.repeat(2000))).toBe('')
      expect(validateSystemPrompt('a'.repeat(2001))).toContain('2000')
      expect(validateSystemPrompt('')).toBe('')
      expect(validateGreeting('a'.repeat(200))).toBe('')
      expect(validateGreeting('a'.repeat(201))).toContain('200')
      expect(validateGreeting('')).toBe('')
    })
  })

  describe('形象图文件 png/jpeg ≤10MB ≤4096px', () => {
    it('类型白名单', () => {
      expect(validateImageFile({ type: 'image/png', size: 1024 })).toBe('')
      expect(validateImageFile({ type: 'image/jpeg', size: 1024 })).toBe('')
      expect(validateImageFile({ type: 'image/svg+xml', size: 1024 })).toContain('PNG / JPEG')
      expect(validateImageFile({ type: 'image/webp', size: 1024 })).toContain('PNG / JPEG')
    })
    it('大小上限', () => {
      expect(validateImageFile({ type: 'image/png', size: IMAGE_MAX_BYTES })).toBe('')
      expect(validateImageFile({ type: 'image/png', size: IMAGE_MAX_BYTES + 1 })).toContain('10MB')
    })
    it('像素上限', () => {
      expect(validateImagePixels(4096, 4096)).toBe('')
      expect(validateImagePixels(4097, 100)).toContain('4096')
      expect(validateImagePixels(100, 4097)).toContain('4096')
    })
  })

  describe('置 active 预检（形象图必填 + 未同步 Vidu 不允许）', () => {
    it('拦截', () => {
      expect(canActivateAvatar({})).toContain('上传形象图')
      expect(canActivateAvatar({ avatar_asset_id: 'a1', vidu_sync_state: { state: 'queued' } })).toContain('同步')
      expect(canActivateAvatar({ avatar_asset_id: 'a1', vidu_sync_state: { state: 'error' } })).toContain('同步')
      expect(canActivateAvatar({ avatar_asset_id: 'a1', vidu_sync_state: { state: 'done' } })).toBe('')
    })
  })
})
