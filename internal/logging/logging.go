// Package logging builds the structured (JSON by default) slog logger.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Options configures New.
type Options struct {
	// Level is debug, info (default), warn/warning or error.
	Level string
	// Format is json (default) or text.
	Format string
	// Service and Version are attached to every record.
	Service string
	Version string
	// Extra receives the same records (for example the OTLP log bridge).
	Extra slog.Handler
}

// ParseLevel converts a textual level into slog.Level. Empty means info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}

// New returns a logger writing to w. Every record carries trace_id and
// span_id when the context holds a valid span, so logs correlate with traces.
func New(w io.Writer, opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	hopts := &slog.HandlerOptions{Level: level}

	var base slog.Handler
	switch strings.ToLower(opts.Format) {
	case "", "json":
		base = slog.NewJSONHandler(w, hopts)
	case "text":
		base = slog.NewTextHandler(w, hopts)
	default:
		return nil, fmt.Errorf("unknown log format %q", opts.Format)
	}

	var h slog.Handler = traceHandler{Handler: base}
	if opts.Extra != nil {
		h = slog.NewMultiHandler(h, levelFilter{Handler: opts.Extra, level: level})
	}

	logger := slog.New(h)
	if opts.Service != "" {
		logger = logger.With(slog.String("service", opts.Service))
	}
	if opts.Version != "" {
		logger = logger.With(slog.String("version", opts.Version))
	}
	return logger, nil
}

// traceHandler injects trace/span identifiers from the context.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}

// levelFilter applies the configured minimum level to handlers that do not
// have their own (such as the OpenTelemetry bridge).
type levelFilter struct {
	slog.Handler
	level slog.Level
}

func (h levelFilter) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level && h.Handler.Enabled(ctx, l)
}

func (h levelFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelFilter{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

func (h levelFilter) WithGroup(name string) slog.Handler {
	return levelFilter{Handler: h.Handler.WithGroup(name), level: h.level}
}
