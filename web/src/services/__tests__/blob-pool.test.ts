import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  BLOB_BUDGET,
  blobCount,
  canAllocateBlob,
  registerBlob,
  releaseBlob,
  releaseBlobs,
  resetBlobPool
} from '../blob-pool'

// objectURL 预算（v3 §9.2）：同屏 ≤8；LRU 驱逐；revoke 正确触发。
describe('blob-pool', () => {
  const created: string[] = []
  const revoked: string[] = []

  beforeEach(() => {
    resetBlobPool()
    created.length = 0
    revoked.length = 0
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: vi.fn(() => {
        const u = `blob:mock-${created.length}`
        created.push(u)
        return u
      }),
      revokeObjectURL: vi.fn((u: string) => {
        revoked.push(u)
      })
    })
  })

  function alloc(key: string, onEvict?: () => void): string {
    const url = URL.createObjectURL(new Blob())
    registerBlob(key, url, onEvict)
    return url
  }

  it('预算内可分配，满后 canAllocateBlob=false', () => {
    for (let i = 0; i < BLOB_BUDGET; i++) alloc(`k${i}`)
    expect(blobCount()).toBe(BLOB_BUDGET)
    expect(canAllocateBlob()).toBe(false)
  })

  it('超预算驱逐 LRU 并回调 onEvict（回退占位）', () => {
    const evicted: string[] = []
    for (let i = 0; i < BLOB_BUDGET; i++) alloc(`k${i}`, () => evicted.push(`k${i}`))
    alloc('k-new')
    expect(blobCount()).toBe(BLOB_BUDGET)
    expect(evicted).toEqual(['k0'])
    expect(revoked).toHaveLength(1)
  })

  it('同 key 换图先释放旧 blob（换图时机）', () => {
    const first = alloc('k1')
    alloc('k1')
    expect(blobCount()).toBe(1)
    expect(revoked).toContain(first)
  })

  it('releaseBlob 释放并回调（卸载/弹层关闭时机）', () => {
    const evicted: string[] = []
    const url = alloc('k1', () => evicted.push('k1'))
    releaseBlob('k1')
    expect(blobCount()).toBe(0)
    expect(revoked).toContain(url)
    expect(evicted).toEqual(['k1'])
  })

  it('releaseBlobs 按前缀批量回收（列表刷新前时机）', () => {
    alloc('avatar-list:a')
    alloc('avatar-list:b')
    alloc('avatar-edit:x')
    releaseBlobs('avatar-list:')
    expect(blobCount()).toBe(1)
  })

  it('LRU 顺序受 touch 影响', async () => {
    const { touchBlob } = await import('../blob-pool')
    const evicted: string[] = []
    for (let i = 0; i < BLOB_BUDGET; i++) alloc(`k${i}`, () => evicted.push(`k${i}`))
    touchBlob('k0') // k0 变为最近使用
    alloc('k-new')
    expect(evicted).toEqual(['k1'])
  })
})
