package contentpkg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDiskFileStorePersistsCurrentAndSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewDiskFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.StageGeneration(ctx, "apply-1", map[string]any{"bgm": "a"})
	if err != nil {
		t.Fatal(err)
	}

	restarted, err := NewDiskFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.FlipCurrent(ctx, AssetRef{}, ref); err != nil {
		t.Fatal(err)
	}
	current, ok, err := restarted.Current(ctx)
	if err != nil || !ok || current != ref {
		t.Fatalf("current=%+v ok=%v err=%v want=%+v", current, ok, err, ref)
	}

	next, err := restarted.StageGeneration(ctx, "apply-2", map[string]any{"bgm": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.FlipCurrent(ctx, ref, next); err != nil {
		t.Fatalf("atomic replacement of existing current: %v", err)
	}
	current, ok, err = restarted.Current(ctx)
	if err != nil || !ok || current != next || next.Revision != ref.Revision+1 {
		t.Fatalf("replacement current=%+v ok=%v err=%v want=%+v", current, ok, err, next)
	}

	raw, err := os.ReadFile(filepath.Join(root, "generations", "apply-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generation struct {
		Snapshot map[string]any `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &generation); err != nil {
		t.Fatal(err)
	}
	if generation.Snapshot["bgm"] != "a" {
		t.Fatalf("snapshot not persisted: %s", raw)
	}
}

func TestDiskFileStoreRejectsEscapingApplyIDs(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	store, err := NewDiskFileStore(filepath.Join(parent, "root"))
	if err != nil {
		t.Fatal(err)
	}
	for _, applyID := range []string{
		"../escape", `..\escape`, "a/b", `a\b`, "/absolute", `C:\drive`, "C:/drive",
		"..", ".", "apply:stream", "apply~1", "apply.", "apply ", "名",
	} {
		t.Run(applyID, func(t *testing.T) {
			if _, err := store.StageGeneration(ctx, applyID, map[string]any{"x": 1}); !errors.Is(err, ErrConflict) {
				t.Fatalf("applyID %q must fail closed: %v", applyID, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(parent, "escape.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path escaped root: %v", err)
	}
}

func TestDiskFileStoreRejectsSymlinkedGenerationDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	store, err := NewDiskFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "generations")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "generations")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.StageGeneration(context.Background(), "apply-1", map[string]any{"x": 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("symlinked generation directory must fail closed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "apply-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write followed symlink outside root: %v", err)
	}
}

func seedDiskCurrent(t *testing.T, ctx context.Context, store *DiskFileStore) AssetRef {
	t.Helper()
	old, err := store.StageGeneration(ctx, "apply-old", map[string]any{"version": "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FlipCurrent(ctx, AssetRef{}, old); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestDiskFileStoreWriteFailureKeepsCurrentPointer(t *testing.T) {
	for name, injected := range map[string]error{"enospc": syscall.ENOSPC, "permission": os.ErrPermission} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewDiskFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			old := seedDiskCurrent(t, ctx, store)
			next, err := store.StageGeneration(ctx, "apply-next", map[string]any{"version": "next"})
			if err != nil {
				t.Fatal(err)
			}
			store.atomicWrite = func(string, []byte) error { return injected }
			if err := store.FlipCurrent(ctx, old, next); !errors.Is(err, injected) {
				t.Fatalf("flip error=%v want=%v", err, injected)
			}
			current, ok, err := store.Current(ctx)
			if err != nil || !ok || current != old {
				t.Fatalf("current changed after failed write: current=%+v ok=%v err=%v old=%+v", current, ok, err, old)
			}
		})
	}
}

func TestDiskFileStoreStageFailureKeepsCurrentPointer(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiskFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := seedDiskCurrent(t, ctx, store)
	store.atomicWrite = func(string, []byte) error { return syscall.ENOSPC }
	if _, err := store.StageGeneration(ctx, "apply-next", map[string]any{"version": "next"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("stage error=%v", err)
	}
	current, ok, err := store.Current(ctx)
	if err != nil || !ok || current != old {
		t.Fatalf("current changed after failed stage: current=%+v ok=%v err=%v old=%+v", current, ok, err, old)
	}
}

func TestDiskFileStoreProbeExecutesEveryTime(t *testing.T) {
	store, err := NewDiskFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	atomicWrite := store.atomicWrite
	store.atomicWrite = func(path string, data []byte) error {
		writes++
		return atomicWrite(path, data)
	}
	for i := 0; i < 2; i++ {
		if err := store.Probe(context.Background()); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if writes != 2 {
		t.Fatalf("probe reused cached result: writes=%d", writes)
	}
	if _, err := os.Stat(filepath.Join(store.root, "probe.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe file was not cleaned: %v", err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "generations" {
			t.Fatalf("probe left temporary artifact %q", entry.Name())
		}
	}
}

func TestDiskFileStoreProbeReportsWriteFailure(t *testing.T) {
	store, err := NewDiskFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.atomicWrite = func(string, []byte) error { return syscall.ENOSPC }
	if err := store.Probe(context.Background()); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("probe error=%v want=ENOSPC", err)
	}
}
