package capability

import (
	"errors"
	"testing"
)

// TestRequireRealFakeRejected §2.3 硬前置：fake 显式拒绝并标注 fake（UI 可见）。
func TestRequireRealFakeRejected(t *testing.T) {
	mx := Matrix{"action_sender": Fake, "song_player": Real, "vidu": Unknown}
	err := mx.RequireReal("dual_avatar", "action_sender", "song_player", "vidu")
	if !errors.Is(err, ErrNotReal) {
		t.Fatalf("非 real 必须拒绝: %v", err)
	}
	var nre *NotRealError
	if !errors.As(err, &nre) {
		t.Fatalf("错误类型: %T", err)
	}
	if nre.Link != "dual_avatar" {
		t.Fatalf("链路标注: %s", nre.Link)
	}
	joined := nre.Error()
	for _, want := range []string{"action_sender=fake", "vidu=unknown"} {
		if !contains(joined, want) {
			t.Fatalf("明细缺 %q: %s", want, joined)
		}
	}
	if contains(joined, "song_player") {
		t.Fatalf("real 能力不得出现在拒绝明细: %s", joined)
	}
}

// TestRequireRealPass 全 real 放行。
func TestRequireRealPass(t *testing.T) {
	mx := Matrix{"a": Real, "b": Real}
	if err := mx.RequireReal("content_pack", "a", "b"); err != nil {
		t.Fatal(err)
	}
}

// TestGetDefaultsUnknown 未登记能力按 unknown（绝不默认 real）。
func TestGetDefaultsUnknown(t *testing.T) {
	var mx Matrix
	if mx.Get("anything") != Unknown {
		t.Fatal("nil 矩阵必须 unknown")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
