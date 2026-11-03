package contentpkg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// MemoryStore 内存实现（dev/测试；语义与 Ent 对齐：CAS 条件更新按 revision+fencing 匹配）。
type MemoryStore struct {
	mu       sync.Mutex
	drafts   map[string]Draft
	journals map[string]Journal
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{drafts: map[string]Draft{}, journals: map[string]Journal{}}
}

func (m *MemoryStore) CreateDraft(_ context.Context, d Draft) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drafts[d.ID] = d
	return nil
}

func (m *MemoryStore) GetDraft(_ context.Context, id string) (Draft, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	return d, ok, nil
}

func (m *MemoryStore) CASUpdateDraft(_ context.Context, id string, expectedRevision, fencingToken int64, mutate func(*Draft)) (Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	if d.Revision != expectedRevision {
		return Draft{}, fmt.Errorf("%w: expected_revision %d != current %d", ErrConflict, expectedRevision, d.Revision)
	}
	if fencingToken < d.FencingToken {
		return Draft{}, fmt.Errorf("%w: token %d < current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	mutate(&d)
	m.drafts[id] = d
	return d, nil
}

func (m *MemoryStore) UpdateDraftLease(_ context.Context, id, ownerID string, leaseUntilMs, newFencingToken int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	if !ok {
		return ErrDraftNotFound
	}
	d.OwnerID = ownerID
	d.LeaseUntilMs = leaseUntilMs
	d.FencingToken = newFencingToken
	m.drafts[id] = d
	return nil
}

func (m *MemoryStore) CreateJournal(_ context.Context, j Journal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.journals[j.ApplyID]; ok {
		return fmt.Errorf("%w: duplicate apply_id %q", ErrConflict, j.ApplyID)
	}
	m.journals[j.ApplyID] = j
	return nil
}

func (m *MemoryStore) GetJournal(_ context.Context, applyID string) (Journal, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.journals[applyID]
	return j, ok, nil
}

func (m *MemoryStore) UpdateJournalStatus(_ context.Context, applyID, status, lastErr string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.journals[applyID]
	if !ok {
		return fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	j.Status = status
	j.LastError = lastErr
	j.UpdatedAtMs = nowMs
	m.journals[applyID] = j
	return nil
}

func (m *MemoryStore) ListJournalsByDraft(_ context.Context, draftID string) ([]Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Journal
	for _, j := range m.journals {
		if j.DraftID == draftID {
			out = append(out, j)
		}
	}
	return out, nil
}

func hashSnapshot(snapshot map[string]any) string {
	if snapshot == nil {
		return ""
	}
	b, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}
