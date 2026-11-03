package refs

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func newPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// F33 列表：只收 .jpg/.jpeg/.png/.webp（蓝本 bridge.py:1348）；目录不存在返回空。
func TestListFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewService(nil, nil)
	if items, err := s.ListFiles(); err != nil || len(items) != 0 {
		t.Fatalf("未配置 dir 返回空, items=%v err=%v", items, err)
	}
	s.SetDir(dir)
	if items, err := s.ListFiles(); err != nil || len(items) != 0 {
		t.Fatalf("空目录, items=%v err=%v", items, err)
	}
	pngBytes := newPNG(t, 8, 8)
	for name, content := range map[string][]byte{
		"a.png": pngBytes, "b.JPG": pngBytes, "c.webp": pngBytes,
		"d.txt": []byte("x"), "e.gif": pngBytes,
	} {
		if err := os.WriteFile(filepath.Join(dir, nameFor(name)), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("应收 3 个图片文件, items=%v", items)
	}
	if items[0].Name != "a.png" || items[0].Size == 0 {
		t.Fatalf("items=%v", items)
	}
}

// F33 上传：校验顺序 = 大小 → 嗅探类型 → 宽高；同名不覆盖加时间戳后缀。
func TestSaveUpload(t *testing.T) {
	dir := t.TempDir()
	s := NewService(nil, nil)
	s.SetDir(dir)

	// 类型拒绝（webp 不允许上传；伪造扩展名也按嗅探拒绝）。
	if _, err := s.SaveUpload("x.webp", bytes.NewReader([]byte("RIFF....WEBP"))); !errors.Is(err, ErrRefType) {
		t.Fatalf("want ErrRefType, got %v", err)
	}
	// 伪造扩展名的真实 png → 落盘扩展名统一为 .png。
	f, err := s.SaveUpload("evil.jpg.png.exe", bytes.NewReader(newPNG(t, 4, 4)))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(f.Name) != ".png" {
		t.Fatalf("扩展名应统一为嗅探结果, name=%q", f.Name)
	}
	// 超大拒绝。
	if _, err := s.SaveUpload("big.png", bytes.NewReader(make([]byte, MaxRefBytes+2))); !errors.Is(err, ErrRefTooLarge) {
		t.Fatalf("want ErrRefTooLarge, got %v", err)
	}
	// 超像素拒绝。
	if _, err := s.SaveUpload("huge.png", bytes.NewReader(newPNG(t, MaxRefPixels+1, 4))); !errors.Is(err, ErrRefPixels) {
		t.Fatalf("want ErrRefPixels, got %v", err)
	}
	// 正常上传 + 同名不覆盖。
	f1, err := s.SaveUpload("ref.jpg", bytes.NewReader(newPNG(t, 4, 4)))
	if err != nil {
		t.Fatal(err)
	}
	f2, err := s.SaveUpload("ref.jpg", bytes.NewReader(newPNG(t, 4, 4)))
	if err != nil {
		t.Fatal(err)
	}
	if f1.Name == f2.Name {
		t.Fatalf("同名应加后缀不覆盖: %q vs %q", f1.Name, f2.Name)
	}
	// 目录穿越被拒绝并落到安全名。
	f3, err := s.SaveUpload("../../etc/passwd.png", bytes.NewReader(newPNG(t, 4, 4)))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Join(dir, f3.Name)) != dir || f3.Name != "passwd.png" {
		t.Fatalf("目录穿越应被剥离, name=%q", f3.Name)
	}
	items, err := s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("落盘后列表应有 4 项, items=%v", items)
	}
}

func nameFor(n string) string { return n }
