package repo

import (
	"context"
	"sync"

	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/auditlog"
)

// AuditEntry 是 repo 层审计记录；为了不依赖 biz 层，字段与 audit.Entry 对齐。
type AuditEntry struct {
	ID        string
	Actor     string
	Route     string
	Action    string
	Target    string
	IP        string
	RequestID string
	TsMs      int64
}

// AuditSink 审计读写端口；repo 自行定义记录类型，避免依赖 biz 层。
type AuditSink interface {
	Append(context.Context, AuditEntry) error
	List(context.Context, int) ([]AuditEntry, error)
}

// EntAuditSink 写 AuditLog 表。
type EntAuditSink struct {
	client *ent.Client
}

func NewEntAuditSink(client *ent.Client) *EntAuditSink {
	return &EntAuditSink{client: client}
}

func (s *EntAuditSink) Append(ctx context.Context, e AuditEntry) error {
	_, err := s.client.AuditLog.Create().
		SetID(e.ID).
		SetActor(e.Actor).
		SetRoute(e.Route).
		SetAction(e.Action).
		SetTarget(e.Target).
		SetIP(e.IP).
		SetRequestID(e.RequestID).
		SetTsMs(e.TsMs).
		Save(ctx)
	return err
}

// List 返回最近 limit 条，按时间倒序。
func (s *EntAuditSink) List(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.client.AuditLog.Query().
		Order(ent.Desc(auditlog.FieldTsMs)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditEntry{
			ID: r.ID, Actor: r.Actor, Route: r.Route, Action: r.Action,
			Target: r.Target, IP: r.IP, RequestID: r.RequestID, TsMs: r.TsMs,
		})
	}
	return out, nil
}

// SwitchableAuditSink 启动时用内存实现；PG 运行期就绪后原子切换为 Ent。
// 降级期间已写入内存的审计不会补写数据库（重启后以 DB 为准）。
type SwitchableAuditSink struct {
	mu  sync.RWMutex
	mem AuditSink
	db  AuditSink
}

func NewSwitchableAuditSink(mem AuditSink) *SwitchableAuditSink {
	return &SwitchableAuditSink{mem: mem}
}

func (s *SwitchableAuditSink) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntAuditSink(client)
	s.mu.Unlock()
}

func (s *SwitchableAuditSink) current() AuditSink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableAuditSink) Append(ctx context.Context, e AuditEntry) error {
	if c := s.current(); c != nil {
		return c.Append(ctx, e)
	}
	return nil
}

func (s *SwitchableAuditSink) List(ctx context.Context, limit int) ([]AuditEntry, error) {
	if c := s.current(); c != nil {
		return c.List(ctx, limit)
	}
	return nil, nil
}
