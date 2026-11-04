package contentpkg

import (
	"context"
	"fmt"
	"sync"
)

// FileStore 是内容包发布所需的最小存储端口。StageGeneration 必须以 applyID 幂等；
// FlipCurrent 必须按 expected 做 CAS，零值 expected 表示当前指针必须不存在。
type FileStore interface {
	Current(ctx context.Context) (AssetRef, bool, error)
	StageGeneration(ctx context.Context, applyID string, snapshot map[string]any) (AssetRef, error)
	FlipCurrent(ctx context.Context, expected, next AssetRef) error
	DiscardGeneration(ctx context.Context, applyID string) error
}

// StorageProbe 对内容存储执行实时读写探测；失败时调用方必须保持 fail-closed。
type StorageProbe interface {
	Probe(ctx context.Context) error
}

// HashSnapshot 返回快照的规范化 JSON SHA-256，供预检识别同 revision 的内容变化。
func HashSnapshot(snapshot map[string]any) string { return hashSnapshot(snapshot) }

type memoryGeneration struct {
	ref  AssetRef
	hash string
}

// MemoryFileStore 提供与 FileStore 合同一致的显式内存实现，适用于开发和测试。
type MemoryFileStore struct {
	mu         sync.RWMutex
	current    AssetRef
	hasCurrent bool
	staged     map[string]memoryGeneration
}

func NewMemoryFileStore() *MemoryFileStore {
	return &MemoryFileStore{staged: map[string]memoryGeneration{}}
}

func (s *MemoryFileStore) Current(_ context.Context) (AssetRef, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.hasCurrent, nil
}

func (s *MemoryFileStore) StageGeneration(_ context.Context, applyID string, snapshot map[string]any) (AssetRef, error) {
	if applyID == "" {
		return AssetRef{}, fmt.Errorf("%w: empty apply id", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := HashSnapshot(snapshot)
	if old, ok := s.staged[applyID]; ok {
		if old.hash != hash {
			return AssetRef{}, fmt.Errorf("%w: apply id %q reused for another generation", ErrConflict, applyID)
		}
		return old.ref, nil
	}
	revision := int64(1)
	if s.hasCurrent {
		revision = s.current.Revision + 1
	}
	ref := AssetRef{ID: applyID, Revision: revision, ContentHash: hash}
	s.staged[applyID] = memoryGeneration{ref: ref, hash: hash}
	return ref, nil
}

func (s *MemoryFileStore) FlipCurrent(_ context.Context, expected, next AssetRef) error {
	if !next.Valid() {
		return fmt.Errorf("%w: next generation is not bound to revision or content hash", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasCurrent && s.current == next {
		return nil
	}
	if expected == (AssetRef{}) {
		if s.hasCurrent {
			return fmt.Errorf("%w: current pointer already exists", ErrConflict)
		}
	} else if !s.hasCurrent || s.current != expected {
		return fmt.Errorf("%w: current pointer changed", ErrConflict)
	}
	staged, ok := s.staged[next.ID]
	if !ok || staged.ref != next {
		return fmt.Errorf("%w: staged generation %+v not found", ErrConflict, next)
	}
	s.current = next
	s.hasCurrent = true
	return nil
}

func (s *MemoryFileStore) DiscardGeneration(_ context.Context, applyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.staged, applyID)
	return nil
}

var _ FileStore = (*MemoryFileStore)(nil)
