package settings

import (
	"context"
	"testing"
)

func TestSaveRequiresAllTwelveGroups(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, _, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 12 {
		t.Fatalf("default groups = %d, want 12", len(groups))
	}
	if _, _, err := svc.Save(context.Background(), groups[:11], 1); err == nil {
		t.Fatal("partial save must be rejected")
	}
}

func TestDanmakuModeValidated(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, rev, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range groups {
		if groups[i].ID == "danmaku" {
			groups[i].Values["mode"] = "bogus"
		}
	}
	if _, _, err := svc.Save(context.Background(), groups, rev); err == nil {
		t.Fatal("invalid danmaku mode must be rejected")
	}
}

func TestEffectiveMatrix(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, rev, _ := svc.Get(context.Background())
	_, reload, err := svc.Save(context.Background(), groups, rev)
	if err != nil {
		t.Fatal(err)
	}
	immediate := map[string]bool{"voice": true, "captions": true, "danmaku": true}
	for _, id := range reload {
		if immediate[id] {
			t.Fatalf("group %s marked next_session but should be immediate", id)
		}
	}
	if len(reload) != 9 {
		t.Fatalf("reload groups = %d, want 9", len(reload))
	}
}
