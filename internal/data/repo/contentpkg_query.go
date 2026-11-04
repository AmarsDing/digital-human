package repo

import (
	"context"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/livedraft"
)

// ListDrafts 在数据库层按 owner 过滤，避免把他人草稿读入服务层。
func (s *EntContentpkgStore) ListDrafts(ctx context.Context, ownerID string) ([]contentpkg.Draft, error) {
	rows, err := s.client.LiveDraft.Query().
		Where(livedraft.OwnerID(ownerID)).
		Order(ent.Desc(livedraft.FieldUpdatedAtMs), ent.Asc(livedraft.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contentpkg.Draft, 0, len(rows))
	for _, row := range rows {
		out = append(out, draftFromEnt(row))
	}
	return out, nil
}

// ListDrafts 保证内存启动与 PG 热切后使用同一 owner 隔离语义。
func (s *SwitchableContentpkgStore) ListDrafts(ctx context.Context, ownerID string) ([]contentpkg.Draft, error) {
	store, ok := s.current().(contentpkg.DraftQueryStore)
	if !ok {
		return nil, contentpkg.ErrDraftQueryUnavailable
	}
	return store.ListDrafts(ctx, ownerID)
}
