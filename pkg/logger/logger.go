package logger

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger that redacts credential-like attributes.
func New(level string, w io.Writer) *slog.Logger {
	if w == nil {
		w = io.Discard
	}
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(redactHandler{next: base})
}

type redactHandler struct {
	next slog.Handler
}

func (h redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(scrub(a))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = scrub(a)
	}
	return redactHandler{next: h.next.WithAttrs(cleaned)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{next: h.next.WithGroup(name)}
}

func scrub(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		cleaned := make([]slog.Attr, len(attrs))
		for i, child := range attrs {
			cleaned[i] = scrub(child)
		}
		a.Value = slog.GroupValue(cleaned...)
		return a
	}
	if sensitive(a.Key) {
		a.Value = slog.StringValue("[redacted]")
	}
	return a
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "password"),
		strings.Contains(k, "token"),
		strings.Contains(k, "secret"),
		strings.Contains(k, "authorization"),
		k == "auth",
		k == "registryauth":
		return true
	default:
		return false
	}
}
