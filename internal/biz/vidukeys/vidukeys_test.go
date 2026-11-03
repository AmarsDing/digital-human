package vidukeys

import (
	"context"
	"errors"
	"testing"

	"github.com/teamwork/digital-human/pkg/secrets"
)

func TestTail8AndMask(t *testing.T) {
	if got := Tail8("vda_1234567890abcdef"); got != "90abcdef" {
		t.Fatalf("Tail8=%q", got)
	}
	if got := Tail8("short"); got != "short" {
		t.Fatalf("Tail8 short=%q", got)
	}
	if got := MaskKey("vda_1234567890abcdef"); got != "vda_12…cdef" {
		t.Fatalf("MaskKey=%q", got)
	}
}

func TestServiceAddValidate(t *testing.T) {
	s := NewService(NewMemoryStore(), nil)
	ctx := context.Background()
	if _, err := s.Add(ctx, "sk-not-vidu", false); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("want ErrInvalidKey, got %v", err)
	}
	if _, err := s.Add(ctx, "vda_short", false); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("want ErrInvalidKey(len<16), got %v", err)
	}
	if !secrets.Available() {
		t.Skip("secrets 不可用（非 Windows 且无 DH_SECRET_KEY），跳过加密链路用例")
	}
	e, err := s.Add(ctx, "vda_1234567890abcdef", false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tail8 != "90abcdef" || e.IsActive {
		t.Fatalf("entry=%+v", e)
	}
	// 重复尾 8 位 → ErrDuplicate。
	if _, err := s.Add(ctx, "vda_zzzzzzzz90abcdef", false); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestServiceActivateAppliesPlain(t *testing.T) {
	if !secrets.Available() {
		t.Skip("secrets 不可用")
	}
	var applied []string
	s := NewService(NewMemoryStore(), func(p string) { applied = append(applied, p) })
	ctx := context.Background()
	k1, k2 := "vda_aaaaaaaakey11111", "vda_bbbbbbbbkey22222"
	if _, err := s.Add(ctx, k1, true); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != k1 {
		t.Fatalf("add(activate) 应立即热改, applied=%v", applied)
	}
	if _, err := s.Add(ctx, k2, false); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("add 非 activate 不应热改, applied=%v", applied)
	}
	if err := s.Activate(ctx, Tail8(k2)); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[1] != k2 {
		t.Fatalf("activate 应解密热改, applied=%v", applied)
	}
	active, keys, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != Tail8(k2) || len(keys) != 2 {
		t.Fatalf("active=%q keys=%v", active, keys)
	}
	if err := s.Activate(ctx, "notexist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestServiceDeleteActiveConflict(t *testing.T) {
	if !secrets.Available() {
		t.Skip("secrets 不可用")
	}
	s := NewService(NewMemoryStore(), nil)
	ctx := context.Background()
	k1, k2 := "vda_aaaaaaaakey11111", "vda_bbbbbbbbkey22222"
	if _, err := s.Add(ctx, k1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, k2, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, Tail8(k1)); !errors.Is(err, ErrActiveKey) {
		t.Fatalf("删 active 应 ErrActiveKey, got %v", err)
	}
	if err := s.Delete(ctx, Tail8(k2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, Tail8(k2)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	_, keys, _ := s.List(ctx)
	if len(keys) != 1 {
		t.Fatalf("keys=%v", keys)
	}
}

func TestServiceAdoptFirstVisit(t *testing.T) {
	if !secrets.Available() {
		t.Skip("secrets 不可用")
	}
	s := NewService(NewMemoryStore(), nil)
	ctx := context.Background()
	adopted, err := s.Adopt(ctx, "vda_1234567890abcdef")
	if err != nil || !adopted {
		t.Fatalf("首访应落库, adopted=%v err=%v", adopted, err)
	}
	adopted, err = s.Adopt(ctx, "vda_zzzzzzzz99999999")
	if err != nil || adopted {
		t.Fatalf("列表非空不应再落库, adopted=%v err=%v", adopted, err)
	}
	active, keys, _ := s.List(ctx)
	if active != "90abcdef" || len(keys) != 1 || !keys[0].IsActive {
		t.Fatalf("active=%q keys=%v", active, keys)
	}
	// 空 key 不落库。
	s2 := NewService(NewMemoryStore(), nil)
	if ok, err := s2.Adopt(ctx, "  "); err != nil || ok {
		t.Fatalf("空 key 不落库, ok=%v err=%v", ok, err)
	}
}

// ApplyActive 把库中 active 热改进 client（启动覆盖 yaml key 的语义）。
func TestServiceApplyActive(t *testing.T) {
	if !secrets.Available() {
		t.Skip("secrets 不可用")
	}
	var applied []string
	s := NewService(NewMemoryStore(), func(p string) { applied = append(applied, p) })
	ctx := context.Background()
	if _, err := s.Add(ctx, "vda_aaaaaaaakey11111", false); err != nil {
		t.Fatal(err)
	}
	// 无 active 行：不动 client。
	if err := s.ApplyActive(ctx); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("无 active 不应热改, applied=%v", applied)
	}
	if _, err := s.Add(ctx, "vda_bbbbbbbbkey22222", true); err != nil {
		t.Fatal(err)
	}
	// Add(activate=true) 已热改一次；清零后单独验证 ApplyActive 的启动覆盖语义。
	applied = applied[:0]
	if err := s.ApplyActive(ctx); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "vda_bbbbbbbbkey22222" {
		t.Fatalf("applied=%v", applied)
	}
}
