package repo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// pgFakeSink 模拟 PG 事件表：EventID 唯一约束（重复返回约束错误）、可整体掉线、
// 可在第 failAt 次成功写入后掉线（模拟补写中途断连）。
type pgFakeSink struct {
	mu     sync.Mutex
	rows   []eventbus.Event
	seen   map[string]bool
	down   bool
	failAt int // >0 时第 failAt 次成功写入后开始失败
	writes int
}

func newPGFakeSink() *pgFakeSink { return &pgFakeSink{seen: map[string]bool{}} }

func (f *pgFakeSink) Append(_ context.Context, e eventbus.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[e.EventID] {
		// (session_id, seq) / event_id 唯一约束冲突：该事件已写入。
		return &ent.ConstraintError{}
	}
	f.writes++
	if f.down || (f.failAt > 0 && f.writes > f.failAt) {
		return errors.New("pg_down")
	}
	f.seen[e.EventID] = true
	f.rows = append(f.rows, e)
	return nil
}

func (f *pgFakeSink) List(context.Context, string, int64, int) ([]eventbus.Event, error) {
	return nil, nil
}

func (f *pgFakeSink) seqs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, 0, len(f.rows))
	for _, e := range f.rows {
		out = append(out, e.Seq)
	}
	return out
}

func seqEv(seq int64) eventbus.Event {
	return eventbus.Event{Seq: seq, SessionID: "s1", EventID: fmt.Sprintf("ev-%d", seq), Type: "song.playing"}
}

// 故障注入：PG 掉线→degraded（写内存不丢）→PG 接上但补写中途再断→恢复重试。
// 断言：降级期写入不丢；补写严格按 seq 顺序；约束冲突幂等跳过已写行；
// 补写失败可整体重试直至补齐，且切换后新事件只写 PG。
func TestSwitchableSinkPGOutageDegradeAndRecovery(t *testing.T) {
	ctx := context.Background()
	mem := live.NewMemorySink(16)
	sw := NewSwitchableSink(mem)

	// 阶段 1：PG 未就绪（degraded），业务写入全进内存，一条不丢。
	for i := int64(1); i <= 5; i++ {
		if err := sw.Append(ctx, seqEv(i)); err != nil {
			t.Fatalf("degraded 期写入失败: %v", err)
		}
	}
	if got := len(mem.All()); got != 5 {
		t.Fatalf("degraded 期内存条数=%d want 5", got)
	}

	// 阶段 2：PG 接上但补写中途再次掉线——第 2 条成功后失败，补写停止。
	db := newPGFakeSink()
	db.failAt = 2
	sw.switchTo(db)
	if got := db.seqs(); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("首次补写应停在第 2 条后: %v", got)
	}

	// 阶段 3：PG 恢复，重试补写——已写的 1、2 靠唯一约束幂等跳过，3..5 按序补齐。
	db.failAt = 0
	sw.switchTo(db)
	if got := db.seqs(); fmt.Sprint(got) != "[1 2 3 4 5]" {
		t.Fatalf("恢复后补写顺序/条数错误: %v", got)
	}

	// 阶段 4：切换后新事件只写 PG，不重复进内存。
	if err := sw.Append(ctx, seqEv(6)); err != nil {
		t.Fatal(err)
	}
	if got := db.seqs(); fmt.Sprint(got) != "[1 2 3 4 5 6]" {
		t.Fatalf("切换后写入: %v", got)
	}
	if got := len(mem.All()); got != 5 {
		t.Fatalf("切换后内存不应再增长: %d", got)
	}
}

// SwitchableCostStore：PG 未就绪时写文件/内存兜底（降级不丢）；
// 切换 Ent 后新写入只进 PG，历史账目按契约不搬移（以兜底存储为准）。
func TestSwitchableCostStoreDegradeThenSwitch(t *testing.T) {
	ctx := context.Background()
	fallback := billing.NewMemoryStore()
	sw := NewSwitchableCostStore(fallback)

	// 降级期：写入落兜底，读得回。
	l1 := billing.Ledger{SessionID: "s-old", CreditsSpent: 10}
	if err := sw.Save(ctx, l1); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := sw.Get(ctx, "s-old"); err != nil || !ok || got.CreditsSpent != 10 {
		t.Fatalf("降级期读: %+v ok=%v err=%v", got, ok, err)
	}

	// PG 就绪切换：新写入只进 PG；历史行按契约留在兜底（Get 以当前 PG 为准）。
	db := billing.NewMemoryStore()
	sw.switchTo(db)
	l2 := billing.Ledger{SessionID: "s-new", CreditsSpent: 7}
	if err := sw.Save(ctx, l2); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.Get(ctx, "s-new"); !ok {
		t.Fatal("切换后新写入必须进 PG")
	}
	if _, ok, _ := fallback.Get(ctx, "s-new"); ok {
		t.Fatal("切换后新写入不得再落兜底")
	}
	if _, ok, _ := sw.Get(ctx, "s-old"); ok {
		t.Fatal("切换瞬间不搬移历史：PG 视角应读不到 s-old")
	}
	if _, ok, _ := fallback.Get(ctx, "s-old"); !ok {
		t.Fatal("历史账目必须仍留在兜底存储（重启不丢）")
	}
}
