package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	requestLogKey
)

func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// RequestLog is a mutable per-request struct attached to the request context
// by the request-id middleware. Downstream middlewares (notably auth) fill in
// identity fields after the handler has run; the access-log middleware reads
// them once the response is written.
type RequestLog struct {
	mu       sync.Mutex
	tenantID string
	tokenID  string
}

func (l *RequestLog) SetIdentity(tenantID, tokenID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.tenantID = tenantID
	l.tokenID = tokenID
	l.mu.Unlock()
}

func (l *RequestLog) Identity() (tenantID, tokenID string) {
	if l == nil {
		return "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tenantID, l.tokenID
}

func ContextWithRequestLog(ctx context.Context, log *RequestLog) context.Context {
	return context.WithValue(ctx, requestLogKey, log)
}

func RequestLogFromContext(ctx context.Context) *RequestLog {
	if v, ok := ctx.Value(requestLogKey).(*RequestLog); ok {
		return v
	}
	return nil
}
