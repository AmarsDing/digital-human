package publish

// DPAPI 加密的目标存储（Windows DPAPI / 其他平台 AES-GCM，见 pkg/secrets）。
// 密文落盘 var/push_target.dat（runtime 目录，已被 .gitignore 覆盖）；
// 明文 key 只存在于进程内存。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/teamwork/digital-human/pkg/secrets"
)

// SecretsFileStore 加密文件目标存储。
type SecretsFileStore struct {
	path string
	mu   sync.Mutex
}

// NewSecretsFileStore 构造；dir 通常为 var/。
func NewSecretsFileStore(dir string) *SecretsFileStore {
	return &SecretsFileStore{path: filepath.Join(dir, "push_target.dat")}
}

type targetFile struct {
	Platform string `json:"platform"`
	RTMPURL  string `json:"rtmp_url"`
	Key      string `json:"key"` // 仅存在于加密文件内部
}

// Load 实现 TargetStore。
func (s *SecretsFileStore) Load() (Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cipher, err := os.ReadFile(s.path)
	if err != nil {
		return Target{}, false
	}
	plain, err := secrets.Decrypt(cipher)
	if err != nil {
		return Target{}, false
	}
	var f targetFile
	if json.Unmarshal(plain, &f) != nil || f.RTMPURL == "" || f.Key == "" {
		return Target{}, false
	}
	return Target{Platform: f.Platform, RTMPURL: f.RTMPURL, Key: f.Key}, true
}

// Save 实现 TargetStore（加密落盘，0600）。
func (s *SecretsFileStore) Save(t Target) error {
	if !secrets.Available() {
		return errors.New("publish: 系统密钥保护不可用，请改用环境变量 DH_STREAM_KEY")
	}
	if t.RTMPURL == "" || t.Key == "" {
		return errors.New("publish: rtmp_url 与 stream_key 均必填")
	}
	plain, err := json.Marshal(targetFile{Platform: t.Platform, RTMPURL: t.RTMPURL, Key: t.Key})
	if err != nil {
		return err
	}
	cipher, err := secrets.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("publish: 密钥加密失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, cipher, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// LayeredStore 优先级：内存（面板刚保存）> 加密文件 > 环境变量。
type LayeredStore struct {
	Mem  *MemTargetStore
	File *SecretsFileStore
	Env  EnvTargetStore
}

// Load 实现 TargetStore。
func (l *LayeredStore) Load() (Target, bool) {
	if l.Mem != nil {
		if t, ok := l.Mem.Load(); ok {
			return t, true
		}
	}
	if l.File != nil {
		if t, ok := l.File.Load(); ok {
			return t, true
		}
	}
	if l.Env.Lookup != nil || true {
		if t, ok := l.Env.Load(); ok {
			return t, true
		}
	}
	return Target{}, false
}

// Save 实现 TargetStore：写内存 + 加密文件（加密不可用时只写内存并告警由调用方处理）。
func (l *LayeredStore) Save(t Target) error {
	if l.Mem != nil {
		if err := l.Mem.Save(t); err != nil {
			return err
		}
	}
	if l.File != nil && secrets.Available() {
		return l.File.Save(t)
	}
	return nil
}
