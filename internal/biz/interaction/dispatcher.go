package interaction

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Handler 副作用执行器：返回下游回执 id（写 downstream_receipt_id）。
type Handler func(ctx context.Context, eff Effect) (receiptID string, err error)

// SourceProvider 崩溃扫描补建缺失 Decision 的原始事件源（§5.5-1）：
// 返回「有原始事件行但无对应 Decision」的源记录键清单；本期骨架——
// 礼物链路在合并点同事务落库，补建主要用于其他链路接入后；nil 时跳过该步。
type SourceProvider func(ctx context.Context) ([]DecisionInput, error)

// Dispatcher dispatcher lease 骨架 + 崩溃扫描（§5.4/§5.5）。
// 并发安全靠 store 层原子 Claim/Mark；单进程内 owner 用实例 id 区分。
type Dispatcher struct {
	store          Store
	owner          string
	lease          time.Duration
	receiptTimeout time.Duration // 超回执期：dispatching 超过该时长无回执 → unknown
	now            func() time.Time
	handlers       map[string]Handler
	sources        SourceProvider
	log            *slog.Logger
}

// NewDispatcher lease/receiptTimeout 为 dispatcher 骨架参数；nil store 报错（fail-closed）。
func NewDispatcher(store Store, lease, receiptTimeout time.Duration, sources SourceProvider) (*Dispatcher, error) {
	if store == nil {
		return nil, errors.New("interaction: dispatcher requires store")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if receiptTimeout <= 0 {
		receiptTimeout = 30 * time.Second
	}
	return &Dispatcher{
		store: store, owner: "disp-" + randHex(4), lease: lease,
		receiptTimeout: receiptTimeout, now: time.Now,
		handlers: map[string]Handler{}, sources: sources,
		log: slog.Default(),
	}, nil
}

// RegisterHandler 注册 effect_type → 执行器（礼物链路注册 gift_thanks）。
func (d *Dispatcher) RegisterHandler(effectType string, h Handler) {
	d.handlers[effectType] = h
}

// DispatchOnce 领取一条 pending（带 lease），执行并写回执/退避；无可派发返回 false。
func (d *Dispatcher) DispatchOnce(ctx context.Context) (bool, error) {
	nowMs := d.now().UnixMilli()
	claimed, err := d.store.ClaimPending(ctx, d.owner, d.lease.Milliseconds(), nowMs, 1)
	if err != nil || len(claimed) == 0 {
		return false, err
	}
	eff := claimed[0]
	dispatchID := "dsp-" + randHex(8)
	leaseUntilMs := nowMs + d.lease.Milliseconds()
	ok, err := d.store.MarkDispatching(ctx, eff.ID, d.owner, dispatchID, leaseUntilMs, nowMs)
	if err != nil || !ok {
		return false, err
	}
	eff.Status = EffectDispatching
	eff.LeaseOwner = d.owner
	eff.LeaseUntilMs = leaseUntilMs
	eff.DispatchID = dispatchID
	eff.Attempt++
	eff.DispatchedAtMs = nowMs
	h, found := d.handlers[eff.EffectType]
	if !found {
		// 无执行器的链路（骨架未接通）：失败终态 + 显式原因，绝不假装成功。
		if err := d.store.MarkFailed(ctx, eff.ID, "no handler for "+eff.EffectType, "handler_missing", nowMs); err != nil {
			return true, err
		}
		return true, nil
	}
	receipt, herr := h(ctx, eff)
	finMs := d.now().UnixMilli()
	if herr != nil {
		if eff.Attempt >= eff.MaxAttempts {
			return true, d.store.MarkFailed(ctx, eff.ID, herr.Error(), "attempts_exhausted", finMs)
		}
		// 固定 1s 退避工程初值（后续切片接指数退避）。
		return true, d.store.MarkFailedRetryable(ctx, eff.ID, herr.Error(), finMs+1000, finMs)
	}
	return true, d.store.MarkSucceeded(ctx, eff.ID, receipt, finMs)
}

// Sweep 崩溃扫描一步（启动时 + 周期调用；§5.5）：
//  1. 补建缺失 Decision（sources 提供原始事件行 → Decide 同事务落库）；
//  2. lease 过期的 dispatching 转 unknown + reconcile_pending，禁止自动重放；
//  3. 超回执期的 dispatching 同样转 unknown + reconcile_pending。
func (d *Dispatcher) Sweep(ctx context.Context) error {
	nowMs := d.now().UnixMilli()
	if d.sources != nil {
		orphans, err := d.sources(ctx)
		if err != nil {
			return fmt.Errorf("interaction: sweep sources: %w", err)
		}
		for _, in := range orphans {
			if _, _, err := NewService(d.store).Decide(ctx, in); err != nil {
				d.log.Warn("interaction sweep decide failed", "source", in.SourceEventRowID, "err", err)
			}
		}
	}
	expired, err := d.store.ListLeaseExpired(ctx, nowMs)
	if err != nil {
		return err
	}
	for _, eff := range expired {
		if err := d.store.MarkUnknown(ctx, eff.ID, "lease_expired", nowMs); err != nil {
			d.log.Warn("interaction sweep mark-unknown failed", "effect", eff.ID, "err", err)
		}
	}
	stale, err := d.store.ListReceiptTimeout(ctx, nowMs-d.receiptTimeout.Milliseconds())
	if err != nil {
		return err
	}
	for _, eff := range stale {
		if err := d.store.MarkUnknown(ctx, eff.ID, "receipt_timeout", nowMs); err != nil {
			d.log.Warn("interaction sweep mark-unknown failed", "effect", eff.ID, "err", err)
		}
	}
	return nil
}

// Reconcile 由操作员在查明下游事实后显式选择终态或重排队。
func (d *Dispatcher) Reconcile(ctx context.Context, id string, action ReconcileAction, receiptID, reason string) error {
	return d.store.Reconcile(ctx, id, action, receiptID, reason, d.now().UnixMilli())
}

// Run 崩溃扫描 goroutine：启动即扫一次，随后每 interval（§5.5 工程初值 30s）扫一次。
// ctx 取消即退出；返回 chan 供测试等待（可为 nil 忽略）。
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		_ = d.Sweep(ctx)
		d.dispatchPending(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := d.Sweep(ctx); err != nil {
					d.log.Warn("interaction sweep failed", "err", err)
				}
				d.dispatchPending(ctx)
			}
		}
	}()
}

func (d *Dispatcher) dispatchPending(ctx context.Context) {
	for {
		worked, err := d.DispatchOnce(ctx)
		if err != nil {
			d.log.Warn("interaction dispatch failed", "err", err)
			return
		}
		if !worked {
			return
		}
	}
}

func newUUID() string { return randHex(16) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
