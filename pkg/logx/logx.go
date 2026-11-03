package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
)

// 敏感字段统一脱敏；任何层都不允许把完整 key / token 写进日志。
var sensitiveKeys = map[string]struct{}{
	"authorization": {}, "api_key": {}, "apikey": {}, "token": {}, "secret": {},
	"password": {}, "vidu_key": {}, "cookie": {}, "set-cookie": {},
}

// New 返回结构化 logger；format=text 仅用于本地排障。
func New(level, format string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if _, ok := sensitiveKeys[strings.ToLower(a.Key)]; ok {
				if a.Value.Kind() == slog.KindString && a.Value.String() != "" {
					return slog.String(a.Key, "***")
				}
			}
			return a
		},
	}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

type ctxKey struct{}

// ridKey 独立于 logger 存放 request_id，保证即使没有 logger 也能贯穿事件与审计。
type ridKey struct{}

// With 把请求级 logger（已带 request_id 等字段）放进 context。
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// WithRequestID 把关联 ID 放进 context；与 logger 注入解耦。
func WithRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, ridKey{}, rid)
}

// RequestID 取当前请求的关联 ID；没有则返回空串。
func RequestID(ctx context.Context) string {
	if rid, ok := ctx.Value(ridKey{}).(string); ok {
		return rid
	}
	return ""
}

// From 取请求级 logger；没有则用默认 logger。
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// NewRequestID 生成 16 字符十六进制关联 ID。
func NewRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req-fallback"
	}
	return hex.EncodeToString(b)
}

// MaskTail 只保留末尾 n 位，用于 Vidu Key 展示。
func MaskTail(s string, n int) string {
	if len(s) <= n {
		return strings.Repeat("*", len(s))
	}
	return strings.Repeat("*", len(s)-n) + s[len(s)-n:]
}
