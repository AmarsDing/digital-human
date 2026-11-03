// Package billing 维护场次成本台账（CostLedger）与积分归属。
//
// 蓝本事实：engine.stats 只有 credits{remain,start,spent,at}、tokens{total,prompt,completion,calls}、
// diamonds/gifts（按场计数，开播清零）；credits 轮询间隔 max(30, credits_poll_s)。
// 蓝本没有“按账号归属”字段，这里以 account_id 作为可选的归属维度（默认 local），
// 汇总仅做加法，不臆造单价；estimated_cost 由调用方给出，来源字段显式标注。
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrSessionID = errors.New("session_id 为空")

type Ledger struct {
	SessionID     string  `json:"session_id"`
	AccountID     string  `json:"account_id"`
	CreditsStart  int64   `json:"credits_start"`
	CreditsEnd    int64   `json:"credits_end"`
	CreditsSpent  int64   `json:"credits_spent"`
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	TokensTotal   int64   `json:"tokens_total"`
	LLMCalls      int64   `json:"llm_calls"`
	Diamonds      int64   `json:"diamonds"`
	GiftCount     int64   `json:"gift_count"`
	EstimatedCost float64 `json:"estimated_cost"`
	CostSource    string  `json:"cost_source,omitempty"`
	StartedAtMs   int64   `json:"started_at_ms,omitempty"`
	EndedAtMs     int64   `json:"ended_at_ms,omitempty"`
	UpdatedAtMs   int64   `json:"updated_at_ms"`
	Source        string  `json:"source"`
}

type Store interface {
	Get(ctx context.Context, sessionID string) (Ledger, bool, error)
	Save(ctx context.Context, l Ledger) error
	List(ctx context.Context) ([]Ledger, error)
}

type MemoryStore struct {
	mu   sync.RWMutex
	rows map[string]Ledger
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{rows: map[string]Ledger{}} }

func (m *MemoryStore) Get(_ context.Context, id string) (Ledger, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.rows[id]
	return l, ok, nil
}

func (m *MemoryStore) Save(_ context.Context, l Ledger) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[l.SessionID] = l
	return nil
}

func (m *MemoryStore) List(context.Context) ([]Ledger, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Ledger, 0, len(m.rows))
	for _, l := range m.rows {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}

// FileStore 落 data/cost/ledger.json，保证重启后复盘不丢。
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) load() (map[string]Ledger, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Ledger{}, nil
		}
		return nil, err
	}
	var rows []Ledger
	if err := json.Unmarshal(raw, &rows); err != nil {
		// 账本损坏不得静默当空：改名备份后报错，后续写入从空账本重建。
		backup := fmt.Sprintf("%s.corrupt-%s", s.path, time.Now().Format("20060102-150405.000"))
		_ = os.Rename(s.path, backup)
		return nil, fmt.Errorf("ledger 文件损坏，已备份到 %s: %w", backup, err)
	}
	out := make(map[string]Ledger, len(rows))
	for _, l := range rows {
		out[l.SessionID] = l
	}
	return out, nil
}

func (s *FileStore) save(rows map[string]Ledger) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	out := make([]Ledger, 0, len(rows))
	for _, l := range rows {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FileStore) Get(_ context.Context, id string) (Ledger, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return Ledger{}, false, err
	}
	l, ok := rows[id]
	return l, ok, nil
}

func (s *FileStore) Save(_ context.Context, l Ledger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	rows[l.SessionID] = l
	return s.save(rows)
}

func (s *FileStore) List(context.Context) ([]Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Ledger, 0, len(rows))
	for _, l := range rows {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// Upsert 写入/更新台账；credits_spent 优先按 start-end 计算，负数按 0 处理。
func (s *Service) Upsert(ctx context.Context, l Ledger) (Ledger, error) {
	l.SessionID = strings.TrimSpace(l.SessionID)
	if l.SessionID == "" {
		return Ledger{}, ErrSessionID
	}
	if l.AccountID == "" {
		l.AccountID = "local"
	}
	if l.CreditsSpent == 0 && l.CreditsStart > 0 && l.CreditsEnd > 0 && l.CreditsEnd <= l.CreditsStart {
		l.CreditsSpent = l.CreditsStart - l.CreditsEnd
	}
	if l.CreditsSpent < 0 {
		l.CreditsSpent = 0
	}
	if l.TokensTotal == 0 {
		l.TokensTotal = l.TokensIn + l.TokensOut
	}
	if l.Source == "" {
		l.Source = "manual"
	}
	if l.CostSource == "" {
		l.CostSource = "unset"
	}
	l.UpdatedAtMs = s.now().UnixMilli()
	return l, s.store.Save(ctx, l)
}

func (s *Service) Get(ctx context.Context, sessionID string) (Ledger, bool, error) {
	return s.store.Get(ctx, sessionID)
}

func (s *Service) List(ctx context.Context) ([]Ledger, error) { return s.store.List(ctx) }

type Totals struct {
	Key           string  `json:"key"`
	Sessions      int     `json:"sessions"`
	CreditsSpent  int64   `json:"credits_spent"`
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	TokensTotal   int64   `json:"tokens_total"`
	LLMCalls      int64   `json:"llm_calls"`
	Diamonds      int64   `json:"diamonds"`
	GiftCount     int64   `json:"gift_count"`
	EstimatedCost float64 `json:"estimated_cost"`
	Estimated     bool    `json:"estimated"`
}

// Summary 按 session 或 account 汇总；estimated=true 表示成本是估算而非账单。
func (s *Service) Summary(ctx context.Context, groupBy string) ([]Totals, error) {
	rows, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	groupBy = strings.ToLower(strings.TrimSpace(groupBy))
	if groupBy != "account" {
		groupBy = "session"
	}
	idx := map[string]*Totals{}
	for _, l := range rows {
		key := l.SessionID
		if groupBy == "account" {
			key = l.AccountID
			if key == "" {
				key = "local"
			}
		}
		t := idx[key]
		if t == nil {
			t = &Totals{Key: key}
			idx[key] = t
		}
		t.Sessions++
		t.CreditsSpent += l.CreditsSpent
		t.TokensIn += l.TokensIn
		t.TokensOut += l.TokensOut
		t.TokensTotal += l.TokensTotal
		t.LLMCalls += l.LLMCalls
		t.Diamonds += l.Diamonds
		t.GiftCount += l.GiftCount
		t.EstimatedCost += l.EstimatedCost
		if l.EstimatedCost > 0 {
			t.Estimated = true
		}
	}
	out := make([]Totals, 0, len(idx))
	for _, t := range idx {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreditsSpent == out[j].CreditsSpent {
			return out[i].Key < out[j].Key
		}
		return out[i].CreditsSpent > out[j].CreditsSpent
	})
	return out, nil
}
