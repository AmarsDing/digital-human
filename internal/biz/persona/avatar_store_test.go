package persona

import (
	"context"
	"errors"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/avatar"
)

// TestAvatarBackedStoreDelegation v3 §1.2 拍板证据：avatar 档案服务为 Persona 行唯一写方，
// F 兼容壳经 AvatarBackedStore 委托后语义不变，且档案字段（vidu_*/greeting 等）读改写保留。
func TestAvatarBackedStoreDelegation(t *testing.T) {
	ctx := context.Background()
	av := avatar.NewMemoryStore()
	svc := NewService(NewAvatarBackedStore(av))

	// 壳路径新建 → 落 avatar 行；壳视角字段往返无损。
	p, err := svc.Save(ctx, "xiaoya", SaveInput{
		Name: "小雅", Tagline: "安静", VoiceID: "voice-a", AvatarNote: "n", SystemPrompt: "你是小雅",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "xiaoya" || p.Name != "小雅" || p.Tagline != "安静" || p.VoiceID != "voice-a" ||
		p.AvatarNote != "n" || p.SystemPrompt != "你是小雅" || p.UpdatedAtMs == 0 {
		t.Fatalf("壳字段往返 = %+v", p)
	}
	// avatar 侧可见同一行（唯一写方落点）。
	a, ok, err := av.Get(ctx, "xiaoya")
	if err != nil || !ok {
		t.Fatalf("avatar 侧缺行: %v %v", ok, err)
	}
	if a.SystemPrompt != "你是小雅" {
		t.Fatalf("system_prompt 落库 = %+v", a)
	}

	// avatar 侧独占字段写入后，壳 Save 不得抹除（读改写保留）。
	a.ViduAvatarID = "vidu-av-1"
	a.ViduImageURI = "vidu://img"
	a.Greeting = "档案问候"
	a.ActionLibraryID = "lib-1"
	if err := av.UpdateWithRevision(ctx, a, a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, "xiaoya", SaveInput{
		Name: "小雅改", Tagline: "t2", VoiceID: "voice-b", SystemPrompt: "改后",
	}); err != nil {
		t.Fatal(err)
	}
	a2, _, _ := av.Get(ctx, "xiaoya")
	if a2.ViduAvatarID != "vidu-av-1" || a2.ViduImageURI != "vidu://img" ||
		a2.Greeting != "档案问候" || a2.ActionLibraryID != "lib-1" {
		t.Fatalf("壳 Save 抹除了档案字段: %+v", a2)
	}
	if a2.Name != "小雅改" || a2.SystemPrompt != "改后" || a2.VoiceID != "voice-b" || a2.Revision != 3 {
		t.Fatalf("壳更新 = %+v", a2)
	}
	// 既有行 slug 不被壳改写（slug 为档案身份，非 uuid 形式时保持）。
	if a2.Slug != "xiaoya" {
		t.Fatalf("壳改写 slug: %+v", a2)
	}

	// 排他 active 与 Select/Delete 语义经委托保持。
	if _, err := svc.Save(ctx, "moli", SaveInput{Name: "茉莉", SystemPrompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Select(ctx, "xiaoya"); err != nil {
		t.Fatal(err)
	}
	list, _, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v %v", len(list), err)
	}
	for _, it := range list {
		want := it.ID == "xiaoya"
		if it.IsActive != want {
			t.Fatalf("排他 active = %+v", list)
		}
	}
	if err := svc.Delete(ctx, "xiaoya"); !errors.Is(err, ErrActiveConflict) {
		t.Fatalf("删 active = %v", err)
	}
	if err := svc.Delete(ctx, "moli"); err != nil {
		t.Fatal(err)
	}
	// 删除为软删：壳视角不可见，avatar 侧行仍在（v1 永不物理删除）。
	if _, err := svc.Get(ctx, "moli"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删后 get = %v", err)
	}
	if _, ok, _ := av.Get(ctx, "moli"); ok {
		t.Fatal("软删后 avatar Get 仍可见")
	}
	if _, total, _ := av.List(ctx, 10, 0, true); total != 2 {
		t.Fatalf("软删行保留 = %d", total)
	}
	// 软删后壳可复用同 ID 重建（slug 复用语义）。
	if _, err := svc.Save(ctx, "moli", SaveInput{Name: "茉莉新", SystemPrompt: "y"}); err != nil {
		t.Fatalf("软删重建 = %v", err)
	}
}

// TestAvatarBackedStoreBackend Backend 透传（memory/ent 标识供 personas 路由 storage 字段）。
func TestAvatarBackedStoreBackend(t *testing.T) {
	s := NewAvatarBackedStore(avatar.NewMemoryStore())
	if s.Backend() != "memory" {
		t.Fatalf("Backend = %s", s.Backend())
	}
}
