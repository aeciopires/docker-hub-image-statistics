package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestDisabledIsNoop(t *testing.T) {
	tel, err := Setup(t.Context(), Options{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tel.TracerProvider.(noop.TracerProvider); !ok {
		t.Fatalf("provider = %T, want noop", tel.TracerProvider)
	}
	if tel.LogHandler != nil {
		t.Fatal("log handler must be nil when disabled")
	}
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidEndpoint(t *testing.T) {
	for _, ep := range []string{"localhost:4318", "ftp://x:1", "http://"} {
		if _, err := Setup(t.Context(), Options{ServiceName: "svc", Traces: true, Endpoint: ep}); err == nil {
			t.Errorf("endpoint %q: expected error", ep)
		}
	}
}

func TestExportsTracesAndLogs(t *testing.T) {
	var traces, logs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/traces":
			traces.Add(1)
		case "/v1/logs":
			logs.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tel, err := Setup(t.Context(), Options{
		ServiceName: "svc", ServiceVersion: "1.0.0", Endpoint: srv.URL,
		Traces: true, Logs: true, SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() != tel.TracerProvider {
		t.Fatal("tracer provider must be registered globally")
	}
	ctx, span := tel.TracerProvider.Tracer("test").Start(t.Context(), "op")
	slog.New(tel.LogHandler).InfoContext(ctx, "hello")
	span.End()

	sctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := tel.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	if traces.Load() == 0 || logs.Load() == 0 {
		t.Fatalf("exports: traces=%d logs=%d", traces.Load(), logs.Load())
	}
}

func TestEnvEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	tel, err := Setup(t.Context(), Options{ServiceName: "svc", Logs: true})
	if err != nil {
		t.Fatal(err)
	}
	if tel.LogHandler == nil {
		t.Fatal("log handler expected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_ = tel.Shutdown(ctx) // nothing listens; must not hang
}
