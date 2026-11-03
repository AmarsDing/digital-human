// Package choreo 实现动作编排的配置 CRUD、运行与暂停。
//
// 蓝本事实：engine.py `_choreo_load/_choreo_loop/_choreo_pause`：
// 随机挑一组 → 组内默认随机（可 order=order）→ 每步 gap（默认 2s，最小 0.3s）→ 组间 gap（默认 15s）；
// 只走动作通道；贵宾动作可暂停编排 4.5s。配置落 data/choreo.json（这里由 Store 端口决定）。
package choreo

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Step struct {
	Cmd  string   `json:"cmd"`
	GapS *float64 `json:"gap_s,omitempty"`
}

type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Order string `json:"order,omitempty"` // order | random；空则用全局
	Steps []Step `json:"steps"`
}

type Config struct {
	Enabled   bool    `json:"enabled"`
	StepGapS  float64 `json:"step_gap_s"`
	GroupGapS float64 `json:"group_gap_s"`
	StepOrder string  `json:"step_order"`
	Groups    []Group `json:"groups"`
}

func DefaultConfig() Config {
	return Config{Enabled: false, StepGapS: 2, GroupGapS: 15, StepOrder: "random", Groups: []Group{}}
}

// normalize 对齐蓝本边界：step_gap 最小持续 0.3s（与 RunOnce 的步内下限一致）、
// group_gap>=1、step_order 默认 random。
func (c Config) normalize() Config {
	out := c
	if out.StepGapS <= 0 {
		out.StepGapS = 2
	} else if out.StepGapS < 0.3 {
		out.StepGapS = 0.3
	}
	if out.GroupGapS < 1 {
		out.GroupGapS = 15
	}
	if out.StepOrder == "" {
		out.StepOrder = "random"
	}
	if out.Groups == nil {
		out.Groups = []Group{}
	}
	return out
}

type Store interface {
	Load() (Config, error)
	Save(Config) error
}

type MemoryStore struct {
	mu  sync.Mutex
	cfg Config
}

func NewMemoryStore(cfg Config) *MemoryStore { return &MemoryStore{cfg: cfg.normalize()} }

func (s *MemoryStore) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.cfg), nil
}

func (s *MemoryStore) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = clone(cfg.normalize())
	return nil
}

// FileStore 对应蓝本 data/choreo.json；文件不存在时返回默认值。
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DefaultConfig(), nil
	}
	return cfg.normalize(), nil
}

func (s *FileStore) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg.normalize(), "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func clone(cfg Config) Config {
	out := cfg
	out.Groups = make([]Group, len(cfg.Groups))
	for i, g := range cfg.Groups {
		cp := g
		cp.Steps = append([]Step(nil), g.Steps...)
		out.Groups[i] = cp
	}
	return out
}

// Sender 与 action.Sender 相同的语义，单独声明避免循环依赖。
type Sender interface {
	SendCommand(ctx context.Context, text string) error
}

var (
	ErrNoGroups = errors.New("没有可运行的编排组")
	ErrNotFound = errors.New("编排组不存在")
)

type Status struct {
	Running      bool   `json:"running"`
	Enabled      bool   `json:"enabled"`
	Group        string `json:"group"`
	Step         int    `json:"step"`
	Runs         int64  `json:"runs"`
	PausedUntil  int64  `json:"paused_until_ms"`
	LastFireAtMs int64  `json:"last_fire_at_ms"`
}

type Service struct {
	store  Store
	sender Sender

	mu         sync.Mutex
	running    bool
	cancel     context.CancelFunc
	pauseUntil time.Time
	runs       int64
	curGroup   string
	curStep    int
	lastFireAt time.Time
	sleep      func(context.Context, time.Duration)
	now        func() time.Time
	rand       *rand.Rand
}

func NewService(store Store, sender Sender) *Service {
	return &Service{
		store: store, sender: sender,
		sleep: func(ctx context.Context, d time.Duration) {
			if d <= 0 {
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
		now:  time.Now,
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *Service) Get() (Config, error) { return s.store.Load() }

func (s *Service) Save(cfg Config) (Config, error) {
	cfg = cfg.normalize()
	return cfg, s.store.Save(cfg)
}

// UpsertGroup 新增或覆盖一组；id 为空时自动生成。
func (s *Service) UpsertGroup(g Group) (Config, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(g.Name) == "" {
		return Config{}, errors.New("编排组名不能为空")
	}
	if g.ID == "" {
		g.ID = "g" + time.Now().Format("150405.000")
	}
	found := false
	for i := range cfg.Groups {
		if cfg.Groups[i].ID == g.ID {
			cfg.Groups[i] = g
			found = true
			break
		}
	}
	if !found {
		cfg.Groups = append(cfg.Groups, g)
	}
	cfg = cfg.normalize()
	return cfg, s.store.Save(cfg)
}

func (s *Service) DeleteGroup(id string) (Config, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return Config{}, err
	}
	found := false
	out := cfg.Groups[:0]
	for _, g := range cfg.Groups {
		if g.ID == id {
			found = true
			continue
		}
		out = append(out, g)
	}
	if !found {
		return cfg, ErrNotFound
	}
	cfg.Groups = out
	return cfg, s.store.Save(cfg)
}

// Start 启动后台编排循环；已在运行时幂等。
func (s *Service) Start(ctx context.Context) bool {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(ctx)
	s.running = true
	s.cancel = cancel
	s.mu.Unlock()
	go s.loop(ctx)
	return true
}

func (s *Service) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = nil
	s.running = false
	s.mu.Unlock()
}

// Pause 高优先级动作独占画面时调用（默认 4.5s）。
func (s *Service) Pause(d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	until := s.now().Add(d)
	if until.After(s.pauseUntil) {
		s.pauseUntil = until
	}
	s.mu.Unlock()
}

func (s *Service) paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.pauseUntil)
}

func (s *Service) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.running = false
			s.cancel = nil
			s.mu.Unlock()
			return
		case <-time.After(500 * time.Millisecond):
		}
		cfg, err := s.store.Load()
		if err != nil || !cfg.Enabled || s.paused() || !s.isRunning() {
			continue
		}
		if _, err := s.RunOnce(ctx); err != nil {
			continue
		}
		s.sleep(ctx, time.Duration(cfg.GroupGapS*float64(time.Second)))
	}
}

func (s *Service) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// RunOnce 运行一组编排并返回实际发送的步数。
func (s *Service) RunOnce(ctx context.Context) (int, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return 0, err
	}
	groups := make([]Group, 0, len(cfg.Groups))
	for _, g := range cfg.Groups {
		if len(g.Steps) > 0 {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return 0, ErrNoGroups
	}
	s.mu.Lock()
	g := groups[s.rand.Intn(len(groups))]
	order := g.Order
	if order == "" {
		order = cfg.StepOrder
	}
	steps := make([]Step, 0, len(g.Steps))
	for _, st := range g.Steps {
		if strings.TrimSpace(st.Cmd) != "" {
			steps = append(steps, st)
		}
	}
	if strings.ToLower(order) == "random" {
		s.rand.Shuffle(len(steps), func(i, j int) { steps[i], steps[j] = steps[j], steps[i] })
	}
	s.curGroup = g.Name
	if s.curGroup == "" {
		s.curGroup = g.ID
	}
	s.mu.Unlock()

	sent := 0
	for i, st := range steps {
		for s.paused() {
			s.sleep(ctx, 200*time.Millisecond)
		}
		if ctx.Err() != nil {
			break
		}
		s.mu.Lock()
		s.curStep = i + 1
		s.mu.Unlock()
		if err := s.sender.SendCommand(ctx, strings.TrimSpace(st.Cmd)); err != nil {
			s.mu.Lock()
			s.curStep = 0
			s.mu.Unlock()
			return sent, err
		}
		sent++
		s.mu.Lock()
		s.lastFireAt = s.now()
		s.mu.Unlock()
		gap := cfg.StepGapS
		if st.GapS != nil {
			gap = *st.GapS
		}
		if gap < 0.3 {
			gap = 0.3
		}
		s.sleep(ctx, time.Duration(gap*float64(time.Second)))
	}
	s.mu.Lock()
	s.curStep = 0
	s.runs++
	s.mu.Unlock()
	return sent, nil
}

func (s *Service) Status() Status {
	cfg, _ := s.store.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Status{
		Running: s.running, Enabled: cfg.Enabled, Group: s.curGroup,
		Step: s.curStep, Runs: s.runs,
	}
	if !s.pauseUntil.IsZero() {
		out.PausedUntil = s.pauseUntil.UnixMilli()
	}
	if !s.lastFireAt.IsZero() {
		out.LastFireAtMs = s.lastFireAt.UnixMilli()
	}
	return out
}
