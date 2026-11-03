package avatar

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestServiceCRUDSemantics 服务层语义（v2 §3.1）：校验/派生 slug/CAS 保留档案字段/
// 删 active 409/duplicate 清空 vidu_*/refs 复制。
func TestServiceCRUDSemantics(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryStore()
	tsvc := NewService(mem)

	// 校验失败：空 name、非法 slug、超长 prompt。
	if _, err := tsvc.Create(ctx, CreateInput{Name: ""}); !errors.Is(err, ErrValidation) {
		t.Fatalf("空 name = %v", err)
	}
	if _, err := tsvc.Create(ctx, CreateInput{Name: "x", Slug: "BAD_SLUG"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("非法 slug = %v", err)
	}
	if _, err := tsvc.Create(ctx, CreateInput{Name: "x", SystemPrompt: strings.Repeat("长", 2001)}); !errors.Is(err, ErrValidation) {
		t.Fatalf("超长 prompt = %v", err)
	}

	// 创建：slug 派生（非 [a-z0-9] 折叠），storage 标识。
	a, err := tsvc.Create(ctx, CreateInput{Name: "小雅 Ava", VoiceID: "voice-a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Slug != "ava" || a.Revision != 1 || a.ViduSync.State != "idle" {
		t.Fatalf("create = %+v", a)
	}
	if tsvc.Storage() != "memory" {
		t.Fatalf("Storage = %s", tsvc.Storage())
	}

	// Update 保留档案独占字段（vidu_*/refs/sync）。
	cur, _, _ := mem.Get(ctx, a.ID)
	cur.ViduAvatarID = "vidu-1"
	cur.ViduImageURI = "vidu://img"
	if err := mem.UpdateWithRevision(ctx, cur, 1); err != nil {
		t.Fatal(err)
	}
	upd, err := tsvc.Update(ctx, a.ID, UpdateInput{Name: "小雅改", Greeting: "hi"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Revision != 3 || upd.ViduAvatarID != "vidu-1" || upd.ViduImageURI != "vidu://img" || upd.Greeting != "hi" {
		t.Fatalf("update 保留 = %+v", upd)
	}
	// CAS 撞 → ErrRevisionConflict。
	if _, err := tsvc.Update(ctx, a.ID, UpdateInput{Name: "z"}, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("CAS 撞 = %v", err)
	}

	// Duplicate：slug 递增、vidu_* 清空、refs 复制。
	if err := mem.PutAsset(ctx, Asset{ID: "img1", SHA256: "s1", Kind: "image"}); err != nil {
		t.Fatal(err)
	}
	if err := tsvc.AddRef(ctx, a.ID, "img1"); err != nil {
		t.Fatal(err)
	}
	dup, err := tsvc.Duplicate(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dup.Slug != "ava-copy" || dup.ViduAvatarID != "" || dup.ViduImageURI != "" ||
		dup.ViduSync.State != "idle" || dup.IsActive || len(dup.Refs) != 1 {
		t.Fatalf("duplicate = %+v", dup)
	}
	dup2, err := tsvc.Duplicate(ctx, a.ID)
	if err != nil || dup2.Slug != "ava-copy2" {
		t.Fatalf("duplicate2 = %+v %v", dup2, err)
	}

	// 排他 active → 删 active → ErrAvatarInUse。
	if err := tsvc.SetActive(ctx, dup.ID); err != nil {
		t.Fatal(err)
	}
	if err := tsvc.Delete(ctx, dup.ID); !errors.Is(err, ErrAvatarInUse) {
		t.Fatalf("删 active = %v", err)
	}
	if err := tsvc.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tsvc.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删后 get = %v", err)
	}

	// AddRef 超限 → ErrTooManyRefs（服务经 store 契约）；用无 refs 的新档案测满 3 张。
	fresh, err := tsvc.Create(ctx, CreateInput{Name: "ref 仓"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		if err := mem.PutAsset(ctx, Asset{ID: id, SHA256: "sha-" + id, Kind: "ref"}); err != nil {
			t.Fatal(err)
		}
		if err := tsvc.AddRef(ctx, fresh.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.PutAsset(ctx, Asset{ID: "r4", SHA256: "sha-r4", Kind: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := tsvc.AddRef(ctx, fresh.ID, "r4"); !errors.Is(err, ErrTooManyRefs) {
		t.Fatalf("第 4 张 = %v", err)
	}
}

// TestImageServicePipeline §5.3 校验链顺序与落点：限长→白名单→像素→dedup→落盘→登记；
// ResolvePath 强制限定存储根内。
func TestImageServicePipeline(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	dir := t.TempDir()
	svc := NewImageService(store, dir)

	// 正常上传：png 落盘 <sha16>.png，登记素材，Path 相对根内。
	png1 := makePNG(t, 8, 8)
	a, dedup, err := svc.SaveUpload(ctx, "image", "a.png", bytes.NewReader(png1))
	if err != nil || dedup {
		t.Fatalf("save = %v %v", dedup, err)
	}
	if a.SHA256 == "" || a.Path == "" || a.Kind != "image" || a.Name != "a.png" {
		t.Fatalf("asset = %+v", a)
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatalf("落盘缺失: %v", err)
	}
	base := filepath.Base(a.Path)
	if len(base) != 20 || !strings.HasSuffix(base, ".png") {
		t.Fatalf("内容寻址命名 = %s", base)
	}
	// 同内容 → dedup 复用首条（不新建文件数量也不变）。
	a2, dedup, err := svc.SaveUpload(ctx, "image", "b.png", bytes.NewReader(png1))
	if err != nil || !dedup || a2.ID != a.ID {
		t.Fatalf("dedup = %+v %v", a2, err)
	}

	// 类型白名单：文本/webp 魔术字节拒绝。
	if _, _, err := svc.SaveUpload(ctx, "image", "x.txt", bytes.NewReader([]byte("hello"))); !errors.Is(err, ErrImageType) {
		t.Fatalf("文本 = %v", err)
	}
	webp := append([]byte("RIFF\x04\x00\x00\x00WEBP"), make([]byte, 32)...)
	if _, _, err := svc.SaveUpload(ctx, "image", "x.webp", bytes.NewReader(webp)); !errors.Is(err, ErrImageType) {
		t.Fatalf("webp = %v", err)
	}
	// 像素超限 → ErrImagePixels。
	if _, _, err := svc.SaveUpload(ctx, "image", "big.png", bytes.NewReader(makePNG(t, 5000, 10))); !errors.Is(err, ErrImagePixels) {
		t.Fatalf("超像素 = %v", err)
	}
	// 超 10MB → ErrImageTooLarge（构造 >10MB  reader）。
	big := make([]byte, MaxImageBytes+2)
	if _, _, err := svc.SaveUpload(ctx, "image", "big.bin", bytes.NewReader(big)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("超长 = %v", err)
	}

	// ResolvePath：命中返回根内绝对路径；DB 外路径拒绝（防目录穿越）。
	p, err := svc.ResolvePath(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(dir)
	if !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		t.Fatalf("路径越根: %s", p)
	}
	if err := store.PutAsset(ctx, Asset{ID: "evil", SHA256: "evil-sha", Kind: "image", Path: filepath.Join("..", "secret.png")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolvePath(ctx, "evil"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目录穿越 = %v", err)
	}
	if _, err := svc.ResolvePath(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在 = %v", err)
	}
}
