package contentpkg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrOwnerRequired         = errors.New("contentpkg: owner_id required")
	ErrDraftQueryUnavailable = errors.New("contentpkg: draft query unavailable")
)

// DraftQueryStore 是按 owner 隔离的草稿只读查询端口，不改变既有 Store 的写契约。
type DraftQueryStore interface {
	ListDrafts(ctx context.Context, ownerID string) ([]Draft, error)
}

// ListOwnedDrafts 只返回 owner 可见的草稿；存储不支持查询时 fail-closed。
func (s *Service) ListOwnedDrafts(ctx context.Context, ownerID string) ([]Draft, error) {
	if strings.TrimSpace(ownerID) == "" {
		return nil, ErrOwnerRequired
	}
	if s.store == nil {
		return nil, ErrDraftQueryUnavailable
	}
	store, ok := s.store.(DraftQueryStore)
	if !ok {
		return nil, ErrDraftQueryUnavailable
	}
	return store.ListDrafts(ctx, ownerID)
}

// GetOwnedDraft 将不存在与越权统一映射为 ErrDraftNotFound，避免按 ID 探测他人草稿。
func (s *Service) GetOwnedDraft(ctx context.Context, draftID, ownerID string) (Draft, error) {
	if strings.TrimSpace(ownerID) == "" {
		return Draft{}, ErrOwnerRequired
	}
	if s.store == nil {
		return Draft{}, ErrDraftQueryUnavailable
	}
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Draft{}, err
	}
	if !ok || d.OwnerID != ownerID {
		return Draft{}, ErrDraftNotFound
	}
	return d, nil
}

// ListDrafts 实现内存查询端口，按最近更新倒序、ID 升序保证稳定分页前顺序。
func (m *MemoryStore) ListDrafts(_ context.Context, ownerID string) ([]Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Draft, 0)
	for _, d := range m.drafts {
		if d.OwnerID == ownerID {
			out = append(out, cloneDraft(d))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs == out[j].UpdatedAtMs {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAtMs > out[j].UpdatedAtMs
	})
	return out, nil
}

// SummarizeDraft 生成列表与预检候选共用的非空人类可读摘要。
func SummarizeDraft(d Draft) string {
	title, _ := d.Snapshot["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(d.TemplateID)
	}
	if title == "" {
		title = d.ID
	}
	if title == "" {
		return "未命名草稿"
	}
	return fmt.Sprintf("%s · %s", title, d.Status)
}
