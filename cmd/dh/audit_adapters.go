package main

import (
	"context"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/data/repo"
)

// auditMemoryBridge 让 biz 内存审计实现适配 repo 层的审计端口；
// repo 不 import biz，转换只发生在 wire 组装层。
type auditMemoryBridge struct {
	mem *audit.MemorySink
}

func (b auditMemoryBridge) Append(ctx context.Context, e repo.AuditEntry) error {
	return b.mem.Append(ctx, toAuditEntry(e))
}

func (b auditMemoryBridge) List(ctx context.Context, limit int) ([]repo.AuditEntry, error) {
	rows, err := b.mem.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]repo.AuditEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, toRepoAuditEntry(e))
	}
	return out, nil
}

// auditSinkBridge 把 biz 审计 Entry 映射到可切换的 repo 审计实现。
type auditSinkBridge struct {
	sw *repo.SwitchableAuditSink
}

func (b auditSinkBridge) Append(ctx context.Context, e audit.Entry) error {
	return b.sw.Append(ctx, toRepoAuditEntry(e))
}

func (b auditSinkBridge) List(ctx context.Context, limit int) ([]audit.Entry, error) {
	rows, err := b.sw.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]audit.Entry, 0, len(rows))
	for _, e := range rows {
		out = append(out, toAuditEntry(e))
	}
	return out, nil
}

func toRepoAuditEntry(e audit.Entry) repo.AuditEntry {
	return repo.AuditEntry{
		ID: e.ID, Actor: e.Actor, Route: e.Route, Action: e.Action,
		Target: e.Target, IP: e.IP, RequestID: e.RequestID, TsMs: e.TsMs,
	}
}

func toAuditEntry(e repo.AuditEntry) audit.Entry {
	return audit.Entry{
		ID: e.ID, Actor: e.Actor, Route: e.Route, Action: e.Action,
		Target: e.Target, IP: e.IP, RequestID: e.RequestID, TsMs: e.TsMs,
	}
}
