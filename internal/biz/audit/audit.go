// Package audit 记录写操作审计。审计只保留路径级元数据，绝不记录请求体、
// 密钥或 token；query 中的敏感值也不会进入审计。
package audit

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry 一条审计记录；字段与 AuditLog 表对齐。
type Entry struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	Route     string `json:"route"`
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	IP        string `json:"ip,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	// Status 响应状态码（HTTP filter 记录；RPC 侧审计为 0）。
	Status int   `json:"status,omitempty"`
	TsMs   int64 `json:"ts_ms"`
}

// Sink 审计落库端口；内存实现用于无 PG 环境。
type Sink interface {
	Append(ctx context.Context, e Entry) error
	List(ctx context.Context, limit int) ([]Entry, error)
}

// MemorySink 进程内环形缓冲：只保留最近 cap 条，上限默认 1000。
type MemorySink struct {
	mu   sync.Mutex
	cap  int
	rows []Entry
}

func NewMemorySink(capacity int) *MemorySink {
	if capacity <= 0 {
		capacity = 1000
	}
	return &MemorySink{cap: capacity}
}

func (s *MemorySink) Append(_ context.Context, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, e)
	if len(s.rows) > s.cap {
		s.rows = s.rows[len(s.rows)-s.cap:]
	}
	return nil
}

// List 返回最近 limit 条，按时间倒序（最新在前）；limit<=0 时返回全部。
func (s *MemorySink) List(_ context.Context, limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.rows)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Entry, 0, limit)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.rows[i])
	}
	return out, nil
}

// Service 负责补齐审计 ID 与时间戳。
type Service struct {
	sink Sink
}

func NewService(sink Sink) *Service { return &Service{sink: sink} }

// Record 写入一条审计；sink 为空时静默丢弃（内存兜底不可用时也不影响主流程）。
func (s *Service) Record(ctx context.Context, e Entry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.TsMs == 0 {
		e.TsMs = time.Now().UnixMilli()
	}
	if s == nil || s.sink == nil {
		return nil
	}
	return s.sink.Append(ctx, e)
}

// List 读取最近 limit 条审计。
func (s *Service) List(ctx context.Context, limit int) ([]Entry, error) {
	if s == nil || s.sink == nil {
		return nil, nil
	}
	return s.sink.List(ctx, limit)
}
