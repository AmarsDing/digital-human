import { apiRequest } from './client'

export interface DraftItem {
  draft_id: string
  revision: number
  status: string
  committed: boolean
  summary: string
  updated_at_ms: number
}

export interface DraftDetail extends DraftItem {
  template_id: string
  owner_id: string
  fencing_token: number
  lease_until_ms: number
  schema_version: number
  snapshot: { title?: string; notes?: string; avatar?: { id: string; revision: number }; [key: string]: unknown }
}

const route = (id: string) => `/api/v1/live-drafts/${encodeURIComponent(id)}`
export async function listDrafts(owner: string): Promise<DraftItem[]> {
  return (await apiRequest<{ drafts: DraftItem[] }>(`/api/v1/live-drafts?owner_id=${encodeURIComponent(owner)}`)).drafts
}
export async function getDraft(id: string, owner: string): Promise<DraftDetail> {
  return (await apiRequest<{ draft: DraftDetail }>(`${route(id)}?owner_id=${encodeURIComponent(owner)}`)).draft
}
export async function createDraft(snapshot: DraftDetail['snapshot']): Promise<DraftDetail> {
  return (await apiRequest<{ draft: DraftDetail }>('/api/v1/live-drafts', {
    method: 'POST', body: { template_id: 'operator-session', base_revision: 1, schema_version: 2, snapshot }
  })).draft
}
export async function openDraft(id: string, owner: string): Promise<DraftDetail> {
  return (await apiRequest<{ draft: DraftDetail }>(`${route(id)}/open-edit`, {
    method: 'POST', body: { owner_id: owner, lease_ttl_seconds: 300 }
  })).draft
}
export async function saveDraft(draft: DraftDetail, snapshot: DraftDetail['snapshot']): Promise<DraftDetail> {
  return (await apiRequest<{ draft: DraftDetail }>(route(draft.draft_id), {
    method: 'PUT', body: { owner_id: draft.owner_id, fencing_token: draft.fencing_token, expected_revision: draft.revision, snapshot }
  })).draft
}
export async function commitDraft(draft: DraftDetail): Promise<DraftDetail> {
  await apiRequest(`${route(draft.draft_id)}/apply`, {
    method: 'POST', timeoutMs: 30000,
    body: {
      owner_id: draft.owner_id, fencing_token: draft.fencing_token, expected_revision: draft.revision,
      // 内容发布关联号；直播的短期预检凭证在提交完成后独立取得。
      preflight_id: `content-${crypto.randomUUID()}`
    }
  })
  return getDraft(draft.draft_id, draft.owner_id)
}
