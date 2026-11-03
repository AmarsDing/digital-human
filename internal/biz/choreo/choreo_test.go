package choreo

import (
	"context"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/action"
)

func fastSvc(cfg Config) (*Service, *action.FakeSender) {
	snd := action.NewFakeSender()
	s := NewService(NewMemoryStore(cfg), snd)
	s.sleep = func(context.Context, time.Duration) {}
	return s, snd
}

func TestConfigNormalize(t *testing.T) {
	s, _ := fastSvc(DefaultConfig())
	cfg, err := s.Save(Config{StepGapS: 0.1, GroupGapS: 0.5, Groups: nil})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StepGapS != 0.3 || cfg.GroupGapS != 15 || cfg.StepOrder != "random" || cfg.Groups == nil {
		t.Fatalf("normalized=%+v", cfg)
	}
	// 0 与负数仍回退默认 2s。
	cfg, err = s.Save(Config{StepGapS: 0})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StepGapS != 2 {
		t.Fatalf("normalized=%+v", cfg)
	}
}

func TestGroupCRUD(t *testing.T) {
	s, _ := fastSvc(DefaultConfig())
	cfg, err := s.UpsertGroup(Group{Name: "开场", Steps: []Step{{Cmd: "挥手"}, {Cmd: "点头"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Groups) != 1 || cfg.Groups[0].ID == "" {
		t.Fatalf("cfg=%+v", cfg)
	}
	cfg, err = s.DeleteGroup(cfg.Groups[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Groups) != 0 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if _, err := s.DeleteGroup("nope"); err == nil {
		t.Fatal("missing group should error")
	}
}

func TestRunOnceSendsAllSteps(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Groups = []Group{{ID: "g1", Name: "热身", Steps: []Step{{Cmd: "左手"}, {Cmd: "右手"}}}}
	s, snd := fastSvc(cfg)
	sent, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("sent=%d", sent)
	}
	if got := snd.Sent(); len(got) != 2 || got[0] == "" || got[1] == "" {
		t.Fatalf("sent=%v", got)
	}
	if st := s.Status(); st.Runs != 1 {
		t.Fatalf("status=%+v", st)
	}
}

func TestRunOnceRequiresGroups(t *testing.T) {
	s, _ := fastSvc(DefaultConfig())
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("empty choreo should error")
	}
}

func TestPauseWindow(t *testing.T) {
	s, _ := fastSvc(DefaultConfig())
	s.Pause(30 * time.Millisecond)
	if !s.paused() {
		t.Fatal("should be paused")
	}
	time.Sleep(40 * time.Millisecond)
	if s.paused() {
		t.Fatal("pause should expire")
	}
	if st := s.Status(); st.PausedUntil != 0 && st.PausedUntil <= time.Now().UnixMilli() {
		// 过期后状态仍保留时间戳，但不影响运行；这里只验证 paused 判断。
	}
}
