package repo

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/costledger"
)

// EntCostStore 实现 billing.Store：一场一行（session_id 唯一），存在即更新。
type EntCostStore struct {
	client *ent.Client
}

func NewEntCostStore(client *ent.Client) *EntCostStore {
	return &EntCostStore{client: client}
}

func (s *EntCostStore) Get(ctx context.Context, sessionID string) (billing.Ledger, bool, error) {
	r, err := s.client.CostLedger.Query().Where(costledger.SessionID(sessionID)).Only(ctx)
	if ent.IsNotFound(err) {
		return billing.Ledger{}, false, nil
	}
	if err != nil {
		return billing.Ledger{}, false, err
	}
	return entLedger(r), true, nil
}

func (s *EntCostStore) Save(ctx context.Context, l billing.Ledger) error {
	exists, err := s.client.CostLedger.Query().Where(costledger.SessionID(l.SessionID)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		_, err := s.client.CostLedger.Update().Where(costledger.SessionID(l.SessionID)).
			SetAccountID(l.AccountID).
			SetCreditsStart(l.CreditsStart).
			SetCreditsEnd(l.CreditsEnd).
			SetCreditsSpent(l.CreditsSpent).
			SetTokensIn(l.TokensIn).
			SetTokensOut(l.TokensOut).
			SetTokensTotal(l.TokensTotal).
			SetLlmCalls(l.LLMCalls).
			SetDiamonds(l.Diamonds).
			SetGiftCount(l.GiftCount).
			SetEstimatedCost(l.EstimatedCost).
			SetCostSource(l.CostSource).
			SetSource(l.Source).
			SetStartedAtMs(l.StartedAtMs).
			SetEndedAtMs(l.EndedAtMs).
			SetUpdatedAtMs(l.UpdatedAtMs).
			Save(ctx)
		return err
	}
	_, err = s.client.CostLedger.Create().
		SetID(uuid.NewString()).
		SetSessionID(l.SessionID).
		SetAccountID(l.AccountID).
		SetCreditsStart(l.CreditsStart).
		SetCreditsEnd(l.CreditsEnd).
		SetCreditsSpent(l.CreditsSpent).
		SetTokensIn(l.TokensIn).
		SetTokensOut(l.TokensOut).
		SetTokensTotal(l.TokensTotal).
		SetLlmCalls(l.LLMCalls).
		SetDiamonds(l.Diamonds).
		SetGiftCount(l.GiftCount).
		SetEstimatedCost(l.EstimatedCost).
		SetCostSource(l.CostSource).
		SetSource(l.Source).
		SetStartedAtMs(l.StartedAtMs).
		SetEndedAtMs(l.EndedAtMs).
		SetUpdatedAtMs(l.UpdatedAtMs).
		Save(ctx)
	return err
}

func (s *EntCostStore) List(ctx context.Context) ([]billing.Ledger, error) {
	rows, err := s.client.CostLedger.Query().Order(ent.Asc(costledger.FieldSessionID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]billing.Ledger, 0, len(rows))
	for _, r := range rows {
		out = append(out, entLedger(r))
	}
	return out, nil
}

func entLedger(r *ent.CostLedger) billing.Ledger {
	return billing.Ledger{
		SessionID:     r.SessionID,
		AccountID:     r.AccountID,
		CreditsStart:  r.CreditsStart,
		CreditsEnd:    r.CreditsEnd,
		CreditsSpent:  r.CreditsSpent,
		TokensIn:      r.TokensIn,
		TokensOut:     r.TokensOut,
		TokensTotal:   r.TokensTotal,
		LLMCalls:      r.LlmCalls,
		Diamonds:      r.Diamonds,
		GiftCount:     r.GiftCount,
		EstimatedCost: r.EstimatedCost,
		CostSource:    r.CostSource,
		StartedAtMs:   r.StartedAtMs,
		EndedAtMs:     r.EndedAtMs,
		UpdatedAtMs:   r.UpdatedAtMs,
		Source:        r.Source,
	}
}

// SwitchableCostStore 启动时用文件账本兜底（无 PG 降级，重启不丢）；
// PG 运行期就绪后原子切换为 Ent。与 JSON 文件账本的差异：
//   - 文件账本整文件重写，Ent 按行 upsert；两者字段一一对应，无精度差。
//   - 切换瞬间不搬移文件中的历史账目（历史数据以文件为准，新写入进 PG）。
type SwitchableCostStore struct {
	mu  sync.RWMutex
	mem billing.Store
	db  billing.Store
}

func NewSwitchableCostStore(fallback billing.Store) *SwitchableCostStore {
	return &SwitchableCostStore{mem: fallback}
}

func (s *SwitchableCostStore) SetEnt(client *ent.Client) {
	s.switchTo(NewEntCostStore(client))
}

// switchTo 原子切换底层实现；SetEnt 的生产路径与测试注入替身共用同一临界区。
func (s *SwitchableCostStore) switchTo(db billing.Store) {
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
}

func (s *SwitchableCostStore) current() billing.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableCostStore) Get(ctx context.Context, sessionID string) (billing.Ledger, bool, error) {
	return s.current().Get(ctx, sessionID)
}

func (s *SwitchableCostStore) Save(ctx context.Context, l billing.Ledger) error {
	return s.current().Save(ctx, l)
}

func (s *SwitchableCostStore) List(ctx context.Context) ([]billing.Ledger, error) {
	return s.current().List(ctx)
}
