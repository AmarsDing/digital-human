package repo

import (
	"context"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/vidukeys"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/vidukey"
)

// EntViduKeyStore 实现 vidukeys.Store（F42/G18）：cipher+tail8 落库，完整 key 永不落明文。
type EntViduKeyStore struct {
	client *ent.Client
}

func NewEntViduKeyStore(client *ent.Client) *EntViduKeyStore {
	return &EntViduKeyStore{client: client}
}

func (s *EntViduKeyStore) List(ctx context.Context) ([]vidukeys.StoredKey, error) {
	rows, err := s.client.ViduKey.Query().Order(ent.Asc(vidukey.FieldAddedAtMs)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]vidukeys.StoredKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, vidukeys.StoredKey{
			ID:        r.ID,
			Cipher:    append([]byte(nil), r.Cipher...),
			Tail8:     r.Tail8,
			IsActive:  r.IsActive,
			AddedAtMs: r.AddedAtMs,
		})
	}
	return out, nil
}

func (s *EntViduKeyStore) Add(ctx context.Context, k vidukeys.StoredKey) error {
	_, err := s.client.ViduKey.Create().
		SetID(k.ID).
		SetCipher(k.Cipher).
		SetTail8(k.Tail8).
		SetIsActive(k.IsActive).
		SetAddedAtMs(k.AddedAtMs).
		Save(ctx)
	return err
}

// SetActive 清其它行后置位 tail8；tail8 为空串时仅清空（服务层 Add(activate) 的先清后插）。
func (s *EntViduKeyStore) SetActive(ctx context.Context, tail8 string) error {
	if tail8 != "" {
		n, err := s.client.ViduKey.Query().Where(vidukey.Tail8(tail8)).Count(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return vidukeys.ErrNotFound
		}
	}
	if _, err := s.client.ViduKey.Update().SetIsActive(false).Where(vidukey.IsActive(true)).Save(ctx); err != nil {
		return err
	}
	if tail8 == "" {
		return nil
	}
	_, err := s.client.ViduKey.Update().SetIsActive(true).Where(vidukey.Tail8(tail8)).Save(ctx)
	return err
}

func (s *EntViduKeyStore) Delete(ctx context.Context, tail8 string) error {
	n, err := s.client.ViduKey.Delete().Where(vidukey.Tail8(tail8)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return vidukeys.ErrNotFound
	}
	return nil
}

// SwitchableViduKeyStore 启动用内存兜底；PG 运行期就绪后原子切换为 Ent（对齐项目热切模式）。
type SwitchableViduKeyStore struct {
	mu  sync.RWMutex
	mem vidukeys.Store
	db  vidukeys.Store
}

func NewSwitchableViduKeyStore(mem vidukeys.Store) *SwitchableViduKeyStore {
	return &SwitchableViduKeyStore{mem: mem}
}

func (s *SwitchableViduKeyStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntViduKeyStore(client)
	s.mu.Unlock()
}

func (s *SwitchableViduKeyStore) current() vidukeys.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableViduKeyStore) List(ctx context.Context) ([]vidukeys.StoredKey, error) {
	return s.current().List(ctx)
}

func (s *SwitchableViduKeyStore) Add(ctx context.Context, k vidukeys.StoredKey) error {
	return s.current().Add(ctx, k)
}

func (s *SwitchableViduKeyStore) SetActive(ctx context.Context, tail8 string) error {
	return s.current().SetActive(ctx, tail8)
}

func (s *SwitchableViduKeyStore) Delete(ctx context.Context, tail8 string) error {
	return s.current().Delete(ctx, tail8)
}
