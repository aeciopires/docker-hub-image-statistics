// Package telemetry configures OpenTelemetry tracing and log export (OTLP/HTTP).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Options configures Setup.
type Options struct {
	ServiceName    string
	ServiceVersion string
	// Endpoint is an OTLP/HTTP base URL (http://host:4318). Empty means the
	// exporters read the standard OTEL_EXPORTER_OTLP_* variables.
	Endpoint    string
	Traces      bool
	Logs        bool
	SampleRatio float64
}

// Telemetry holds the configured providers.
type Telemetry struct {
	// TracerProvider is a no-op provider when tracing is disabled.
	TracerProvider trace.TracerProvider
	// LogHandler forwards slog records to OTLP; nil when log export is disabled.
	LogHandler slog.Handler

	shutdowns []func(context.Context) error
}

// Setup creates the providers and registers the tracer provider and the
// W3C trace-context propagator globally.
func Setup(ctx context.Context, opts Options) (*Telemetry, error) {
	t := &Telemetry{TracerProvider: noop.NewTracerProvider()}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if !opts.Traces && !opts.Logs {
		otel.SetTracerProvider(t.TracerProvider)
		return t, nil
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(opts.ServiceName),
		semconv.ServiceVersion(opts.ServiceVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	var endpoint *url.URL
	if opts.Endpoint != "" {
		endpoint, err = url.Parse(opts.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, fmt.Errorf("telemetry: invalid OTLP endpoint %q", opts.Endpoint)
		}
	}

	if opts.Traces {
		var topts []otlptracehttp.Option
		if endpoint != nil {
			topts = append(topts, otlptracehttp.WithEndpointURL(endpoint.JoinPath("v1", "traces").String()))
		}
		exp, err := otlptracehttp.New(ctx, topts...)
		if err != nil {
			return nil, fmt.Errorf("trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))),
		)
		t.TracerProvider = tp
		t.shutdowns = append(t.shutdowns, tp.Shutdown)
	}
	otel.SetTracerProvider(t.TracerProvider)

	if opts.Logs {
		var lopts []otlploghttp.Option
		if endpoint != nil {
			lopts = append(lopts, otlploghttp.WithEndpointURL(endpoint.JoinPath("v1", "logs").String()))
		}
		exp, err := otlploghttp.New(ctx, lopts...)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("log exporter: %w", err), t.Shutdown(ctx))
		}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
			sdklog.WithResource(res),
		)
		t.LogHandler = otelslog.NewHandler(opts.ServiceName, otelslog.WithLoggerProvider(lp))
		t.shutdowns = append(t.shutdowns, lp.Shutdown)
	}
	return t, nil
}

// Shutdown flushes and stops every provider.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, fn := range t.shutdowns {
		errs = append(errs, fn(ctx))
	}
	t.shutdowns = nil
	return errors.Join(errs...)
}
