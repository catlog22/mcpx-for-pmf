// Package logging provides a unified structured logger for MCPX.
//
// Format (text, default):
//
//	2026-07-30T05:40:00.123Z INFO  msg=listening component=server addr=127.0.0.1:9090
//
// Format (json, MCPX_LOG_FORMAT=json):
//
//	{"time":"...","level":"INFO","msg":"listening","component":"server","addr":"..."}
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	mu     sync.RWMutex
	logger *slog.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
)

// Options configures the process logger.
type Options struct {
	Level  string // debug|info|warn|error
	Format string // text|json
	Out    io.Writer
}

// Init sets the global logger. Safe to call once at process start.
func Init(opts Options) {
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	level := parseLevel(opts.Level)
	if opts.Level == "" {
		if v := os.Getenv("MCPX_LOG_LEVEL"); v != "" {
			level = parseLevel(v)
		}
	}
	format := strings.ToLower(opts.Format)
	if format == "" {
		format = strings.ToLower(os.Getenv("MCPX_LOG_FORMAT"))
	}
	ho := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(out, ho)
	} else {
		h = slog.NewTextHandler(out, ho)
	}
	mu.Lock()
	logger = slog.New(h)
	mu.Unlock()
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func L() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

// With returns a child logger with attributes (e.g. component=server).
func With(args ...any) *slog.Logger {
	return L().With(args...)
}

func Debug(msg string, args ...any) { L().Debug(msg, args...) }
func Info(msg string, args ...any)  { L().Info(msg, args...) }
func Warn(msg string, args ...any)  { L().Warn(msg, args...) }
func Error(msg string, args ...any) { L().Error(msg, args...) }

func DebugContext(ctx context.Context, msg string, args ...any) {
	L().DebugContext(ctx, msg, args...)
}
func InfoContext(ctx context.Context, msg string, args ...any) {
	L().InfoContext(ctx, msg, args...)
}
func ErrorContext(ctx context.Context, msg string, args ...any) {
	L().ErrorContext(ctx, msg, args...)
}
