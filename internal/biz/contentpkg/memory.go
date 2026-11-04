package contentpkg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// MemoryStore 与 Ent 使用相同的 CAS、账本进度和恢复枚举语义。
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
	if _, ok := m.drafts[d.ID]; ok {
		return fmt.Errorf("%w: duplicate draft id %q", ErrConflict, d.ID)
	}
	m.drafts[d.ID] = cloneDraft(d)
	return nil
}

func (m *MemoryStore) GetDraft(_ context.Context, id string) (Draft, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	return cloneDraft(d), ok, nil
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
	if fencingToken != d.FencingToken {
		return Draft{}, fmt.Errorf("%w: token %d != current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	d = cloneDraft(d)
	mutate(&d)
	m.drafts[id] = cloneDraft(d)
	return cloneDraft(d), nil
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
	m.journals[j.ApplyID] = cloneJournal(j)
	return nil
}

func (m *MemoryStore) GetJournal(_ context.Context, applyID string) (Journal, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.journals[applyID]
	return cloneJournal(j), ok, nil
}

func (m *MemoryStore) UpdateJournal(_ context.Context, applyID, expectedStatus string, mutate func(*Journal)) (Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.journals[applyID]
	if !ok {
		return Journal{}, fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	if j.Status != expectedStatus {
		return Journal{}, fmt.Errorf("%w: journal status %q != expected %q", ErrConflict, j.Status, expectedStatus)
	}
	j = cloneJournal(j)
	mutate(&j)
	if j.Status == JournalCommitted && j.CommittedAtMs == 0 {
		j.CommittedAtMs = j.UpdatedAtMs
	}
	if j.Status == JournalRolledBack && j.RolledBackAtMs == 0 {
		j.RolledBackAtMs = j.UpdatedAtMs
	}
	m.journals[applyID] = cloneJournal(j)
	return cloneJournal(j), nil
}

func (m *MemoryStore) UpdateJournalStatus(_ context.Context, applyID, status, lastErr string, nowMs int64) error {
	_, err := m.updateJournalStatus(applyID, status, lastErr, nowMs)
	return err
}

func (m *MemoryStore) updateJournalStatus(applyID, status, lastErr string, nowMs int64) (Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.journals[applyID]
	if !ok {
		return Journal{}, fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	j.Status = status
	j.LastError = lastErr
	j.UpdatedAtMs = nowMs
	if status == JournalCommitted {
		j.CommittedAtMs = nowMs
	}
	if status == JournalRolledBack {
		j.RolledBackAtMs = nowMs
	}
	m.journals[applyID] = j
	return cloneJournal(j), nil
}

func (m *MemoryStore) ListJournalsByDraft(_ context.Context, draftID string) ([]Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Journal
	for _, j := range m.journals {
		if j.DraftID == draftID {
			out = append(out, cloneJournal(j))
		}
	}
	sortJournals(out)
	return out, nil
}

func (m *MemoryStore) ListPendingJournals(_ context.Context) ([]Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Journal
	for _, j := range m.journals {
		pending := j.Status == JournalPreparing || j.Status == JournalStaging || j.Status == JournalCommitPending
		if j.Status == JournalCommitted {
			pending = j.LastError != ""
			for _, step := range j.Steps {
				if step.Status != "done" {
					pending = true
				}
			}
			if d, ok := m.drafts[j.DraftID]; !ok || d.Status != DraftCommitted {
				pending = true
			}
		}
		if j.Status == JournalRolledBack {
			if d, ok := m.drafts[j.DraftID]; ok && d.Status != DraftRolledBack && d.Status != DraftEditing {
				pending = true
			}
		}
		if pending {
			out = append(out, cloneJournal(j))
		}
	}
	sortJournals(out)
	return out, nil
}

func sortJournals(journals []Journal) {
	sort.Slice(journals, func(i, k int) bool {
		if journals[i].CreatedAtMs == journals[k].CreatedAtMs {
			return journals[i].ApplyID < journals[k].ApplyID
		}
		return journals[i].CreatedAtMs < journals[k].CreatedAtMs
	})
}

func cloneDraft(d Draft) Draft {
	d.Snapshot = cloneMap(d.Snapshot)
	return d
}

func cloneJournal(j Journal) Journal {
	j.Snapshot = cloneMap(j.Snapshot)
	j.OldValues = cloneMap(j.OldValues)
	j.Steps = append([]ApplyStep(nil), j.Steps...)
	return j
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneValue(v[i])
		}
		return out
	case []AssetRef:
		return append([]AssetRef(nil), v...)
	case map[string]AssetRef:
		out := make(map[string]AssetRef, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out
	default:
		return value
	}
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

var _ Store = (*MemoryStore)(nil)
var _ JournalStore = (*MemoryStore)(nil)
var _ JournalRecoveryStore = (*MemoryStore)(nil)
