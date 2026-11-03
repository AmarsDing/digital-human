package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/setting"
)

// revisionRowID 是设置 revision 的保留行；不参与 13 组业务设置。
const revisionRowID = "__revision"

// EntSettingStore 实现 settings.Store：13 组设置各自一行，revision 存于保留行。
type EntSettingStore struct {
	client *ent.Client
}

func NewEntSettingStore(client *ent.Client) *EntSettingStore {
	return &EntSettingStore{client: client}
}

// Load 按 DefaultGroups 顺序返回 13 组；库里缺失的组用默认值补齐，未知 id 忽略。
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
	decryptLLMAPIKey(out)
	return out, revision, nil
}

// decryptLLMAPIKey 把 llm 组落库密文解为内存明文（仅内存态，密文不出存储层）。
// 明文行（迁移前的旧数据）原样透传；带 enc:v1: 前缀但解密失败（密钥不可用/密文损坏）
// fail-closed 置空——消费方 llm client 拿到空 key 返回 ErrLLMNotConfigured，
// 绝不把密文当明文上抛，也不因此让整组设置不可用。
func decryptLLMAPIKey(groups []settings.Group) {
	for i := range groups {
		if groups[i].ID != "llm" {
			continue
		}
		v, _ := groups[i].Values["api_key"].(string)
		if !isEncryptedSettingSecret(v) {
			continue
		}
		plain, err := decryptSettingSecret(v)
		if err != nil {
			slog.Default().Warn("settings: llm.api_key 解密失败，按未配置处理", "err", err)
			groups[i].Values["api_key"] = ""
			continue
		}
		groups[i].Values["api_key"] = plain
	}
}

// encryptLLMAPIKeyValue 落库前加密明文 api_key；空值/已带 enc:v1: 前缀的密文原样
// 通过（幂等，重复 Save 不会双重加密）。加密不可用（非 Windows 缺 DH_SECRET_KEY）
// 返回错误 fail-closed，宁可 Save 失败也绝不落明文。
func encryptLLMAPIKeyValue(v any) (any, error) {
	s, _ := v.(string)
	if s == "" || isEncryptedSettingSecret(s) {
		return v, nil
	}
	return encryptSettingSecret(s)
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
		vals := cloneValues(g.Values)
		if g.ID == "llm" {
			enc, err := encryptLLMAPIKeyValue(vals["api_key"])
			if err != nil {
				return revision, fmt.Errorf("settings: llm.api_key 加密失败（不落明文）: %w", err)
			}
			vals["api_key"] = enc
		}
		exists, err := cli.Setting.Query().Where(setting.ID(g.ID)).Exist(ctx)
		if err != nil {
			return revision, err
		}
		if exists {
			if _, err := cli.Setting.UpdateOneID(g.ID).
				SetValueJSON(vals).
				SetUpdatedAtMs(now).
				SetUpdatedBy("local").
				Save(ctx); err != nil {
				return revision, err
			}
			continue
		}
		if _, err := cli.Setting.Create().
			SetID(g.ID).
			SetValueJSON(vals).
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

// validateGroups 复用 biz/settings 的 13 组与取值约束。
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
		return errors.New("必须提交全部 13 组设置")
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
