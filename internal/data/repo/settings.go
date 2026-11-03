package repo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/setting"
)

// revisionRowID 是设置 revision 的保留行；不参与 12 组业务设置。
const revisionRowID = "__revision"

// EntSettingStore 实现 settings.Store：12 组设置各自一行，revision 存于保留行。
type EntSettingStore struct {
	client *ent.Client
}

func NewEntSettingStore(client *ent.Client) *EntSettingStore {
	return &EntSettingStore{client: client}
}

// Load 按 DefaultGroups 顺序返回 12 组；库里缺失的组用默认值补齐，未知 id 忽略。
func (s *EntSettingStore) Load(ctx context.Context) ([]settings.Group, int64, error) {
	rows, err := s.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, 0, err
	}
	stored := make(map[string]map[string]any, len(rows))
	revision := int64(1)
	for _, r := range rows {
		if r.ID == revisionRowID {
			revision = revisionFrom(r.ValueJSON)
			continue
		}
		stored[r.ID] = r.ValueJSON
	}

	def := settings.DefaultGroups()
	out := make([]settings.Group, 0, len(def))
	for _, g := range def {
		if vals, ok := stored[g.ID]; ok && vals != nil {
			g.Values = cloneValues(vals)
		}
		out = append(out, g)
	}
	return out, revision, nil
}

// Save 在单个事务内校验 baseRevision 并逐组 upsert，最后推进 revision。
func (s *EntSettingStore) Save(ctx context.Context, groups []settings.Group, baseRevision int64) (int64, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	cli := tx.Client()
	revision, err := s.currentRevision(ctx, cli)
	if err != nil {
		return 0, err
	}
	if baseRevision != 0 && baseRevision != revision {
		return revision, fmt.Errorf("%w: base=%d current=%d", settings.ErrRevisionConflict, baseRevision, revision)
	}
	if err := validateGroups(groups); err != nil {
		return revision, err
	}

	now := time.Now().UnixMilli()
	for _, g := range groups {
		exists, err := cli.Setting.Query().Where(setting.ID(g.ID)).Exist(ctx)
		if err != nil {
			return revision, err
		}
		if exists {
			if _, err := cli.Setting.UpdateOneID(g.ID).
				SetValueJSON(cloneValues(g.Values)).
				SetUpdatedAtMs(now).
				SetUpdatedBy("local").
				Save(ctx); err != nil {
				return revision, err
			}
			continue
		}
		if _, err := cli.Setting.Create().
			SetID(g.ID).
			SetValueJSON(cloneValues(g.Values)).
			SetUpdatedAtMs(now).
			SetUpdatedBy("local").
			Save(ctx); err != nil {
			return revision, err
		}
	}

	next := revision + 1
	if err := s.upsertRevision(ctx, cli, next, now); err != nil {
		return revision, err
	}
	if err := tx.Commit(); err != nil {
		return revision, err
	}
	committed = true
	return next, nil
}

func (s *EntSettingStore) currentRevision(ctx context.Context, cli *ent.Client) (int64, error) {
	row, err := cli.Setting.Query().Where(setting.ID(revisionRowID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 1, nil
		}
		return 0, err
	}
	return revisionFrom(row.ValueJSON), nil
}

func (s *EntSettingStore) upsertRevision(ctx context.Context, cli *ent.Client, revision, now int64) error {
	vals := map[string]any{"revision": revision}
	exists, err := cli.Setting.Query().Where(setting.ID(revisionRowID)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		_, err = cli.Setting.UpdateOneID(revisionRowID).
			SetValueJSON(vals).
			SetUpdatedAtMs(now).
			SetUpdatedBy("local").
			Save(ctx)
		return err
	}
	_, err = cli.Setting.Create().
		SetID(revisionRowID).
		SetValueJSON(vals).
		SetUpdatedAtMs(now).
		SetUpdatedBy("local").
		Save(ctx)
	return err
}

// revisionFrom 兼容 JSON 反序列化得到的 float64 与进程内写入的 int64。
func revisionFrom(v map[string]any) int64 {
	switch n := v["revision"].(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 1
}

// validateGroups 复用 biz/settings 的 12 组与取值约束。
func validateGroups(groups []settings.Group) error {
	def := settings.DefaultGroups()
	known := make(map[string]struct{}, len(def))
	for _, g := range def {
		known[g.ID] = struct{}{}
	}
	for _, g := range groups {
		if _, ok := known[g.ID]; !ok {
			return errors.New("unknown settings group: " + g.ID)
		}
	}
	if len(groups) != len(def) {
		return errors.New("必须提交全部 12 组设置")
	}
	return settings.Validate(groups)
}

func cloneValues(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SwitchableSettingStore 启动时用内存实现；PG 运行期就绪后原子切换为 Ent。
// db 模式下 Save 失败直接返回错误，不回退内存。
type SwitchableSettingStore struct {
	mu  sync.RWMutex
	mem settings.Store
	db  settings.Store
}

func NewSwitchableSettingStore(mem settings.Store) *SwitchableSettingStore {
	return &SwitchableSettingStore{mem: mem}
}

func (s *SwitchableSettingStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntSettingStore(client)
	s.mu.Unlock()
}

func (s *SwitchableSettingStore) current() settings.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableSettingStore) Load(ctx context.Context) ([]settings.Group, int64, error) {
	if c := s.current(); c != nil {
		return c.Load(ctx)
	}
	return nil, 0, nil
}

func (s *SwitchableSettingStore) Save(ctx context.Context, groups []settings.Group, baseRevision int64) (int64, error) {
	if c := s.current(); c != nil {
		return c.Save(ctx, groups, baseRevision)
	}
	return 0, nil
}
