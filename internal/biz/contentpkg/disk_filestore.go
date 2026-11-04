package contentpkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type diskGeneration struct {
	Ref      AssetRef       `json:"ref"`
	Hash     string         `json:"hash"`
	Snapshot map[string]any `json:"snapshot"`
}

// DiskFileStore 将 generation 与 current 指针持久化到同一根目录。
type DiskFileStore struct {
	mu          sync.Mutex
	root        string
	atomicWrite func(path string, data []byte) error
}

func NewDiskFileStore(root string) (*DiskFileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: empty content storage root", ErrUnavailable)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve content storage root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "generations"), 0o700); err != nil {
		return nil, fmt.Errorf("create content storage root: %w", err)
	}
	store := &DiskFileStore{root: abs, atomicWrite: writeFileAtomic}
	if err := store.checkDirectories(); err != nil {
		return nil, err
	}
	return store, nil
}

func validApplyID(applyID string) bool {
	if len(applyID) == 0 || len(applyID) > 128 {
		return false
	}
	for i := 0; i < len(applyID); i++ {
		c := applyID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect content storage directory: %v", ErrUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: content storage directory is not a real directory", ErrUnavailable)
	}
	return nil
}

func (s *DiskFileStore) checkDirectories() error {
	if err := requireRealDirectory(s.root); err != nil {
		return err
	}
	return requireRealDirectory(filepath.Join(s.root, "generations"))
}

func (s *DiskFileStore) generationPath(applyID string) string {
	return filepath.Join(s.root, "generations", applyID+".json")
}

func (s *DiskFileStore) currentPath() string {
	return filepath.Join(s.root, "current.json")
}

func readDiskGeneration(path string) (diskGeneration, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return diskGeneration{}, false, nil
	}
	if err != nil {
		return diskGeneration{}, false, err
	}
	var generation diskGeneration
	if err := json.Unmarshal(raw, &generation); err != nil {
		return diskGeneration{}, false, err
	}
	return generation, true, nil
}

func (s *DiskFileStore) readCurrent() (AssetRef, bool, error) {
	generation, ok, err := readDiskGeneration(s.currentPath())
	if err != nil || !ok {
		return AssetRef{}, false, err
	}
	return generation.Ref, true, nil
}

func (s *DiskFileStore) Current(_ context.Context) (AssetRef, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDirectories(); err != nil {
		return AssetRef{}, false, err
	}
	return s.readCurrent()
}

func (s *DiskFileStore) StageGeneration(_ context.Context, applyID string, snapshot map[string]any) (AssetRef, error) {
	if !validApplyID(applyID) {
		return AssetRef{}, fmt.Errorf("%w: invalid apply id", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDirectories(); err != nil {
		return AssetRef{}, err
	}
	hash := HashSnapshot(snapshot)
	path := s.generationPath(applyID)
	if old, ok, err := readDiskGeneration(path); err != nil {
		return AssetRef{}, err
	} else if ok {
		if old.Hash != hash {
			return AssetRef{}, fmt.Errorf("%w: apply id %q reused for another generation", ErrConflict, applyID)
		}
		return old.Ref, nil
	}
	current, hasCurrent, err := s.readCurrent()
	if err != nil {
		return AssetRef{}, err
	}
	revision := int64(1)
	if hasCurrent {
		revision = current.Revision + 1
	}
	generation := diskGeneration{
		Ref:      AssetRef{ID: applyID, Revision: revision, ContentHash: hash},
		Hash:     hash,
		Snapshot: cloneMap(snapshot),
	}
	raw, err := json.Marshal(generation)
	if err != nil {
		return AssetRef{}, err
	}
	if err := s.atomicWrite(path, raw); err != nil {
		return AssetRef{}, err
	}
	return generation.Ref, nil
}

func (s *DiskFileStore) FlipCurrent(_ context.Context, expected, next AssetRef) error {
	if !next.Valid() {
		return fmt.Errorf("%w: next generation is not bound to revision or content hash", ErrConflict)
	}
	if !validApplyID(next.ID) {
		return fmt.Errorf("%w: invalid apply id", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDirectories(); err != nil {
		return err
	}
	current, hasCurrent, err := s.readCurrent()
	if err != nil {
		return err
	}
	if hasCurrent && current == next {
		return nil
	}
	if expected == (AssetRef{}) {
		if hasCurrent {
			return fmt.Errorf("%w: current pointer already exists", ErrConflict)
		}
	} else if !hasCurrent || current != expected {
		return fmt.Errorf("%w: current pointer changed", ErrConflict)
	}
	staged, ok, err := readDiskGeneration(s.generationPath(next.ID))
	if err != nil {
		return err
	}
	if !ok || staged.Ref != next {
		return fmt.Errorf("%w: staged generation %+v not found", ErrConflict, next)
	}
	raw, err := json.Marshal(diskGeneration{Ref: next})
	if err != nil {
		return err
	}
	return s.atomicWrite(s.currentPath(), raw)
}

func (s *DiskFileStore) DiscardGeneration(_ context.Context, applyID string) error {
	if !validApplyID(applyID) {
		return fmt.Errorf("%w: invalid apply id", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDirectories(); err != nil {
		return err
	}
	current, hasCurrent, err := s.readCurrent()
	if err != nil {
		return err
	}
	if hasCurrent && current.ID == applyID {
		return nil
	}
	if err := os.Remove(s.generationPath(applyID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Probe 每次真实执行写入、读取、原子替换与清理，不缓存历史结果。
func (s *DiskFileStore) Probe(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDirectories(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(s.root, "probe.json")
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(path)
		}
	}()
	payload := []byte("content-storage-write-read-rename-cleanup")
	if err := s.atomicWrite(path, payload); err != nil {
		return err
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, payload) {
		return fmt.Errorf("content storage probe readback mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	removed = true
	return nil
}

func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".contentpkg-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

var _ FileStore = (*DiskFileStore)(nil)
