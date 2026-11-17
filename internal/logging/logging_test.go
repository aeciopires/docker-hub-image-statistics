package logging

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"": slog.LevelInfo, "INFO": slog.LevelInfo, "debug": slog.LevelDebug,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("expected error for unknown level")
	}
}

func TestNewJSONDefaultInfo(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(&buf, Options{Service: "svc", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	logger.Info("shown", "k", "v")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (debug filtered), got %d: %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	for k, want := range map[string]any{"level": "INFO", "msg": "shown", "k": "v", "service": "svc", "version": "1.0.0"} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
}

func TestNewTextAndErrors(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(&buf, Options{Format: "text", Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	logger.WithGroup("g").With("a", 1).Debug("hello")
	if !strings.Contains(buf.String(), "msg=hello") || !strings.Contains(buf.String(), "g.a=1") {
		t.Fatalf("unexpected text output: %q", buf.String())
	}
	if _, err := New(&buf, Options{Format: "xml"}); err == nil {
		t.Error("expected format error")
	}
	if _, err := New(&buf, Options{Level: "nope"}); err == nil {
		t.Error("expected level error")
	}
}

func TestTraceCorrelationAndExtraHandler(t *testing.T) {
	var out, extra bytes.Buffer
	extraHandler := slog.NewJSONHandler(&extra, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger, err := New(&out, Options{Level: "warn", Extra: extraHandler})
	if err != nil {
		t.Fatal(err)
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(t.Context(), sc)

	l := logger.With("x", 1).WithGroup("grp")
	l.InfoContext(ctx, "filtered everywhere")
	l.WarnContext(ctx, "kept")

	if strings.Contains(extra.String(), "filtered") || strings.Contains(out.String(), "filtered") {
		t.Fatal("level filter must apply to all handlers")
	}
	if !strings.Contains(extra.String(), "kept") {
		t.Fatal("extra handler did not receive the record")
	}
	if !strings.Contains(out.String(), sc.TraceID().String()) || !strings.Contains(out.String(), sc.SpanID().String()) {
		t.Fatalf("missing trace correlation: %s", out.String())
	}
}
