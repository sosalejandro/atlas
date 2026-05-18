package shared

import (
	"context"
	"io"
	"log/slog"
)

// Logger is the thin interface every Atlas package depends on for
// structured logging.
//
// Production code wires log/slog (stdlib). Tests use NopLogger. Per
// docs/architecture.md §8 we deliberately avoid go.uber.org/zap — slog is
// good enough and the dep tax matters when external consumers (bmad-cli)
// import packages/ as a library.
type Logger interface {
	Debug(ctx context.Context, msg string, args ...any)
	Info(ctx context.Context, msg string, args ...any)
	Warn(ctx context.Context, msg string, args ...any)
	Error(ctx context.Context, msg string, args ...any)
}

// NopLogger discards all log records. Use in tests and in library callers
// that don't need Atlas's logging at all.
type NopLogger struct{}

func (NopLogger) Debug(context.Context, string, ...any) {}
func (NopLogger) Info(context.Context, string, ...any)  {}
func (NopLogger) Warn(context.Context, string, ...any)  {}
func (NopLogger) Error(context.Context, string, ...any) {}

// slogAdapter wraps *slog.Logger to satisfy Logger.
type slogAdapter struct {
	l *slog.Logger
}

// NewSlogLogger returns a Logger backed by slog.New(slog.NewJSONHandler(w, nil)).
// Pass io.Discard for a silent-but-typed logger.
func NewSlogLogger(w io.Writer) Logger {
	return &slogAdapter{l: slog.New(slog.NewJSONHandler(w, nil))}
}

// NewSlogLoggerFromHandler returns a Logger using the caller-supplied
// slog.Handler. Useful in tests that want to capture records.
func NewSlogLoggerFromHandler(h slog.Handler) Logger {
	return &slogAdapter{l: slog.New(h)}
}

func (a *slogAdapter) Debug(ctx context.Context, msg string, args ...any) {
	a.l.DebugContext(ctx, msg, args...)
}
func (a *slogAdapter) Info(ctx context.Context, msg string, args ...any) {
	a.l.InfoContext(ctx, msg, args...)
}
func (a *slogAdapter) Warn(ctx context.Context, msg string, args ...any) {
	a.l.WarnContext(ctx, msg, args...)
}
func (a *slogAdapter) Error(ctx context.Context, msg string, args ...any) {
	a.l.ErrorContext(ctx, msg, args...)
}
