package repo

import (
	"context"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/patron"
	"github.com/teamwork/digital-human/internal/data/ent"
	entpatron "github.com/teamwork/digital-human/internal/data/ent/patron"
)

// EntPatronStore 实现 patron.Store（F29）：名单全量读写，nick 唯一。
type EntPatronStore struct {
	client *ent.Client
}

func NewEntPatronStore(client *ent.Client) *EntPatronStore {
	return &EntPatronStore{client: client}
}

func (s *EntPatronStore) List(ctx context.Context) ([]patron.Entry, error) {
	rows, err := s.client.Patron.Query().Order(ent.Asc(entpatron.FieldCreatedAtMs)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]patron.Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, patron.Entry{
			Nick: r.Nick, UID: r.UID, Alias: r.Alias, Note: r.Note, Since: r.Since,
		})
	}
	return out, nil
}

// Replace 全量替换（事务内清表重写；名单体量小，与蓝本 save 语义一致）。
func (s *EntPatronStore) Replace(ctx context.Context, items []patron.Entry) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Patron.Delete().Exec(ctx); err != nil {
		return rollback(tx, err)
	}
	now := time.Now().UnixMilli()
	for _, e := range items {
		if err := tx.Patron.Create().
			SetID(e.Nick).
			SetNick(e.Nick).
			SetUID(e.UID).
			SetAlias(e.Alias).
			SetNote(e.Note).
			SetSince(e.Since).
			SetCreatedAtMs(now).
			Exec(ctx); err != nil {
			return rollback(tx, err)
		}
	}
	return tx.Commit()
}

func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return rerr
	}
	return err
}

// SwitchablePatronStore 启动用内存兜底；PG 运行期就绪后原子切换为 Ent（对齐项目热切模式）。
type SwitchablePatronStore struct {
	mu  sync.RWMutex
	mem patron.Store
	db  patron.Store
}

func NewSwitchablePatronStore(mem patron.Store) *SwitchablePatronStore {
	return &SwitchablePatronStore{mem: mem}
}

func (s *SwitchablePatronStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = NewEntPatronStore(client)
}

func (s *SwitchablePatronStore) current() patron.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchablePatronStore) List(ctx context.Context) ([]patron.Entry, error) {
	return s.current().List(ctx)
}

func (s *SwitchablePatronStore) Replace(ctx context.Context, items []patron.Entry) error {
	return s.current().Replace(ctx, items)
}
