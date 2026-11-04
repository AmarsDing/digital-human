import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const getDraft = vi.fn()
vi.mock('../../services/api/drafts', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/drafts')>('../../services/api/drafts')
  return { ...actual, getDraft: (...args: unknown[]) => getDraft(...args) }
})

import { usePreparationStore } from '../preparation'
import type { DraftDetail } from '../../services/api/drafts'

function draft(committed: boolean): DraftDetail {
  return {
    draft_id: 'draft-1', revision: 2, status: committed ? 'committed' : 'editing', committed,
    summary: '晚间试播', updated_at_ms: 100, template_id: 'operator-session', owner_id: 'operator-1',
    fencing_token: 3, lease_until_ms: 1000, schema_version: 2,
    snapshot: { title: '晚间试播', avatar: { id: 'p1', revision: 7 } }
  }
}

describe('preparation store', () => {
  beforeEach(() => {
    localStorage.clear()
    getDraft.mockReset()
    setActivePinia(createPinia())
  })

  it('只持久化已提交草稿 ID，重新加载时回源取得版本与快照', async () => {
    const first = usePreparationStore()
    await first.load()
    first.select(draft(false))
    expect(first.selected).toBeNull()
    expect(localStorage.getItem('dh.prepare.selection.v1')).toBeNull()

    first.select(draft(true))
    expect(localStorage.getItem('dh.prepare.selection.v1')).toBe('draft-1')
    expect(localStorage.getItem('dh.prepare.selection.v1')).not.toContain('晚间试播')

    getDraft.mockResolvedValue({ ...draft(true), revision: 3 })
    setActivePinia(createPinia())
    const restored = usePreparationStore()
    await restored.load()
    expect(getDraft).toHaveBeenCalledWith('draft-1', first.owner)
    expect(restored.selected?.revision).toBe(3)
    expect(restored.selected?.snapshot.avatar?.id).toBe('p1')
  })

  it('持久选择回源失败时清空选择，不使用旧快照兜底', async () => {
    localStorage.setItem('dh.prepare.selection.v1', 'draft-1')
    getDraft.mockRejectedValue(new Error('not found'))
    const store = usePreparationStore()
    await store.load()

    expect(store.selected).toBeNull()
    expect(localStorage.getItem('dh.prepare.selection.v1')).toBeNull()
  })
})
