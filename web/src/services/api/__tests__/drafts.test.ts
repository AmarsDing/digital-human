import { beforeEach, describe, expect, it, vi } from 'vitest'

const request = vi.fn()
vi.mock('../client', async () => {
  const actual = await vi.importActual<typeof import('../client')>('../client')
  return { ...actual, apiRequest: (...args: unknown[]) => request(...args) }
})

import { commitDraft, saveDraft, type DraftDetail } from '../drafts'

const editing: DraftDetail = {
  draft_id: 'draft-1', revision: 2, status: 'editing', committed: false, summary: '试播', updated_at_ms: 100,
  template_id: 'operator-session', owner_id: 'operator-1', fencing_token: 9, lease_until_ms: 1000,
  schema_version: 2, snapshot: { title: '试播' }
}

describe('live drafts API', () => {
  beforeEach(() => request.mockReset())

  it('保存携带 owner、fencing token 与预期版本', async () => {
    request.mockResolvedValue({ draft: editing })
    const snapshot = { title: '新场次', notes: '核对素材' }
    await saveDraft(editing, snapshot)

    expect(request).toHaveBeenCalledWith('/api/v1/live-drafts/draft-1', {
      method: 'PUT',
      body: { owner_id: 'operator-1', fencing_token: 9, expected_revision: 2, snapshot }
    })
  })

  it('提交后重新读取详情，不使用提交前版本作为已提交结果', async () => {
    const committed = { ...editing, revision: 3, status: 'committed', committed: true }
    request.mockResolvedValueOnce({}).mockResolvedValueOnce({ draft: committed })
    const result = await commitDraft(editing)

    expect(request.mock.calls[0][0]).toBe('/api/v1/live-drafts/draft-1/apply')
    expect(request.mock.calls[0][1]).toMatchObject({
      method: 'POST',
      body: { owner_id: 'operator-1', fencing_token: 9, expected_revision: 2 }
    })
    expect(request.mock.calls[1]).toEqual(['/api/v1/live-drafts/draft-1?owner_id=operator-1'])
    expect(result).toEqual(committed)
  })
})
