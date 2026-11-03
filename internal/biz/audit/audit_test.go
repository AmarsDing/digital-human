package audit

import (
	"context"
	"testing"
)

func TestMemorySinkRingLimit(t *testing.T) {
	s := NewMemorySink(3)
	svc := NewService(s)
	for i := 0; i < 5; i++ {
		if err := svc.Record(context.Background(), Entry{Route: "/api/v1/x", Action: "POST"}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	rows, err := svc.List(context.Background(), 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("ring cap: want 3 rows, got %d", len(rows))
	}
}

func TestServiceFillsIDAndTsMs(t *testing.T) {
	s := NewMemorySink(10)
	svc := NewService(s)
	if err := svc.Record(context.Background(), Entry{Actor: "local", Route: "/api/v1/a", Action: "POST"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, _ := svc.List(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].ID == "" || rows[0].TsMs == 0 {
		t.Fatalf("id/ts not filled: %+v", rows[0])
	}
}

func TestListNewestFirst(t *testing.T) {
	s := NewMemorySink(10)
	svc := NewService(s)
	for _, r := range []string{"/a", "/b", "/c"} {
		_ = svc.Record(context.Background(), Entry{Route: r, Action: "POST"})
	}
	rows, _ := svc.List(context.Background(), 2)
	if len(rows) != 2 || rows[0].Route != "/c" || rows[1].Route != "/b" {
		t.Fatalf("newest first: %+v", rows)
	}
}
