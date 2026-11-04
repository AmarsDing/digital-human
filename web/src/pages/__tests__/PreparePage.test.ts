import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const h = vi.hoisted(() => ({
  push: vi.fn(), listAvatars: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(), createDraft: vi.fn(),
  openDraft: vi.fn(), saveDraft: vi.fn(), commitDraft: vi.fn()
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRouter: () => ({ push: h.push }) }
})
vi.mock('../../services/api/panels', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/panels')>('../../services/api/panels')
  return { ...actual, listAvatars: h.listAvatars }
})
vi.mock('../../services/api/drafts', async () => {
  const actual = await vi.importActual<typeof import('../../services/api/drafts')>('../../services/api/drafts')
  return {
    ...actual, listDrafts: h.listDrafts, getDraft: h.getDraft, createDraft: h.createDraft,
    openDraft: h.openDraft, saveDraft: h.saveDraft, commitDraft: h.commitDraft
  }
})

import PreparePage from '../PreparePage.vue'
import { ApiError } from '../../services/api/client'
import { usePreparationStore } from '../../stores/preparation'
import type { DraftDetail } from '../../services/api/drafts'

function draft(committed = false): DraftDetail {
  return {
    draft_id: 'draft-1', revision: committed ? 2 : 1, status: committed ? 'committed' : 'editing', committed,
    summary: '晚间试播', updated_at_ms: 100, template_id: 'operator-session', owner_id: 'operator-1',
    fencing_token: 4, lease_until_ms: 1000, schema_version: 2,
    snapshot: { title: '晚间试播', notes: '', avatar: { id: 'p1', revision: 7 } }
  }
}

const avatar = { persona_id: 'p1', name: '主播', revision: 7, is_active: true, vidu_sync_state: { state: 'done' } }

describe('PreparePage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    setActivePinia(createPinia())
    h.listAvatars.mockResolvedValue({ avatars: [avatar], total: 1, storage: 'sqlite' })
    h.listDrafts.mockResolvedValue([])
    h.getDraft.mockResolvedValue(draft())
    h.createDraft.mockResolvedValue(draft())
    h.openDraft.mockResolvedValue(draft())
    h.saveDraft.mockResolvedValue(draft())
    h.commitDraft.mockResolvedValue(draft(true))
  })

  it('新建、取得租约、提交并选择真实返回的已提交草稿，不自动开播', async () => {
    const w = mount(PreparePage)
    await flushPromises()
    await w.get('input[placeholder="例如：晚间互动试播"]').setValue('晚间试播')
    await w.get('select').setValue('p1')
    await w.findAll('button').find((button) => button.text() === '保存并提交')!.trigger('click')
    await flushPromises()

    expect(h.createDraft).toHaveBeenCalledWith(expect.objectContaining({
      title: '晚间试播', avatar: { id: 'p1', revision: 7 }
    }))
    expect(h.openDraft).toHaveBeenCalledWith('draft-1', expect.any(String))
    expect(h.commitDraft).toHaveBeenCalledWith(draft())
    expect(usePreparationStore().selected).toEqual(draft(true))
    expect(w.text()).toContain('尚未开播')
    expect(h.push).not.toHaveBeenCalledWith('/')
    w.unmount()
  })

  it('草稿版本冲突时保留编辑现场并提示重新打开，不继续提交', async () => {
    h.listDrafts.mockResolvedValue([draft()])
    h.openDraft.mockRejectedValue(new ApiError(409, 'draft_revision_conflict', 'conflict'))
    const w = mount(PreparePage)
    await flushPromises()
    await w.get('.prepare-list button').trigger('click')
    await flushPromises()
    await w.get('input[placeholder="例如：晚间互动试播"]').setValue('保留这次编辑')
    await w.findAll('button').find((button) => button.text() === '保存草稿')!.trigger('click')
    await flushPromises()

    expect(w.get('[role="alert"]').text()).toContain('草稿已更新或编辑权限已过期')
    expect((w.get('input[placeholder="例如：晚间互动试播"]').element as HTMLInputElement).value).toBe('保留这次编辑')
    expect(h.commitDraft).not.toHaveBeenCalled()
    w.unmount()
  })
})
