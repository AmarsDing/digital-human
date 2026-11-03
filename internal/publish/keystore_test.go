package publish

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/teamwork/digital-human/pkg/secrets"
)

func TestSecretsFileStoreRoundTrip(t *testing.T) {
	if !secrets.Available() {
		t.Skip("密钥保护不可用")
	}
	dir := t.TempDir()
	s := NewSecretsFileStore(dir)
	if _, ok := s.Load(); ok {
		t.Fatal("空存储不应命中")
	}
	want := Target{Platform: "douyin", RTMPURL: "rtmp://push.example/live", Key: testStreamKey}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	// 落盘文件必须是密文
	raw, err := os.ReadFile(filepath.Join(dir, "push_target.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || bytes.Contains(raw, []byte(testStreamKey)) {
		t.Fatal("密钥文件出现明文 key")
	}
	got, ok := s.Load()
	if !ok || got != want {
		t.Fatalf("roundtrip: %+v ok=%v", got, ok)
	}
}

func TestLayeredStorePrecedence(t *testing.T) {
	if !secrets.Available() {
		t.Skip("密钥保护不可用")
	}
	l := &LayeredStore{
		Mem:  &MemTargetStore{},
		File: NewSecretsFileStore(t.TempDir()),
		Env: EnvTargetStore{Lookup: func(k string) string {
			if k == "DH_RTMP_URL" {
				return "rtmp://env/live"
			}
			if k == "DH_STREAM_KEY" {
				return "env_key"
			}
			return ""
		}},
	}
	// env 兜底
	t1, ok := l.Load()
	if !ok || t1.Key != "env_key" {
		t.Fatalf("env 兜底: %+v", t1)
	}
	// 面板保存后内存优先
	if err := l.Save(Target{RTMPURL: "rtmp://panel/live", Key: "panel_key"}); err != nil {
		t.Fatal(err)
	}
	t2, _ := l.Load()
	if t2.Key != "panel_key" {
		t.Fatalf("内存优先: %+v", t2)
	}
	// 重启（清内存）后加密文件命中
	l2 := &LayeredStore{Mem: &MemTargetStore{}, File: l.File, Env: l.Env}
	t3, ok := l2.Load()
	if !ok || t3.Key != "panel_key" {
		t.Fatalf("加密文件命中: %+v", t3)
	}
}
