package main

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data"
	"github.com/teamwork/digital-human/internal/data/repo"
)

func TestStoragePersistentCapabilityTracksPostgresProbe(t *testing.T) {
	sessions := repo.NewSwitchableSessionStore(live.NewMemorySessionStore())
	sessions.SetEnt(nil)
	postgresReady := true
	probe := func(context.Context) data.Status {
		return data.Status{Postgres: postgresReady}
	}

	if got := storagePersistentCapability(context.Background(), sessions, probe); got != capability.Real {
		t.Fatalf("Ent 已安装且 PG 可达时 storage_persistent=%s, want=real", got)
	}
	postgresReady = false
	if got := storagePersistentCapability(context.Background(), sessions, probe); got != capability.Unknown {
		t.Fatalf("PG 不可达时 storage_persistent=%s, want=unknown", got)
	}
	postgresReady = true
	if got := storagePersistentCapability(context.Background(), sessions, probe); got != capability.Real {
		t.Fatalf("PG 恢复后 storage_persistent=%s, want=real", got)
	}
}

func TestStoragePersistentCapabilityMemoryRemainsFake(t *testing.T) {
	sessions := repo.NewSwitchableSessionStore(live.NewMemorySessionStore())
	probe := func(context.Context) data.Status { return data.Status{Postgres: true} }
	if got := storagePersistentCapability(context.Background(), sessions, probe); got != capability.Fake {
		t.Fatalf("内存 SessionStore 不得因 PG 可达冒充 real: %s", got)
	}
}
