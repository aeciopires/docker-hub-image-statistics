// Package app wires every component together and implements the CLI.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/aeciopires/docker-hub-image-statistics/internal/collector"
	"github.com/aeciopires/docker-hub-image-statistics/internal/config"
	"github.com/aeciopires/docker-hub-image-statistics/internal/dockerhub"
	"github.com/aeciopires/docker-hub-image-statistics/internal/logging"
	"github.com/aeciopires/docker-hub-image-statistics/internal/metrics"
	"github.com/aeciopires/docker-hub-image-statistics/internal/server"
	"github.com/aeciopires/docker-hub-image-statistics/internal/store"
	"github.com/aeciopires/docker-hub-image-statistics/internal/telemetry"
	"github.com/aeciopires/docker-hub-image-statistics/internal/version"
	"github.com/aeciopires/docker-hub-image-statistics/internal/web"
)

// Env abstracts process inputs/outputs so Main is testable.
type Env struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	// OnListen, when set, receives the bound HTTP address (tests use ":0").
	OnListen func(addr string)
	// Reload receives a value whenever the configuration must be reloaded
	// (main wires it to SIGHUP). Nil disables reloading.
	Reload <-chan struct{}
}

// ErrReload is the cancellation cause used to stop a run for a configuration reload.
var ErrReload = errors.New("configuration reload")

// serve loads the configuration (file, DHIS_* variables and flags), runs the
// application and restarts it with a freshly loaded configuration on every
// reload request. Invalid configurations are rejected.
func serve(ctx context.Context, env Env, flags *pflag.FlagSet) error {
	cfg, used, err := config.Load(flags)
	if err != nil {
		return err
	}
	var opts runOptions
	for {
		runCtx, stop := context.WithCancelCause(ctx)
		done := make(chan error, 1)
		go func() { done <- run(runCtx, cfg, used, env, opts) }()

		next, nextUsed, reload, err := waitForReload(ctx, env, flags, done)
		stop(ErrReload)
		if !reload {
			return err
		}
		// Wait for the current run to shut down before starting the new one,
		// so the HTTP port and the SQLite file are released.
		if err := <-done; err != nil {
			return err
		}
		opts = runOptions{forceCollect: targetsChanged(cfg.DockerHub, next.DockerHub)}
		cfg, used = next, nextUsed
		slog.Default().InfoContext(ctx, "configuration reloaded, restarting components",
			"config_file", used, "targets_changed", opts.forceCollect)
	}
}

// waitForReload blocks until the run ends (reload=false) or a valid new
// configuration arrives (reload=true). Invalid configurations are rejected and
// the current run keeps going. Command-line flags keep their precedence.
func waitForReload(ctx context.Context, env Env, flags *pflag.FlagSet, done <-chan error) (config.Config, string, bool, error) {
	for {
		select {
		case err := <-done:
			return config.Config{}, "", false, err
		case <-env.Reload:
			cfg, used, err := config.Load(flags)
			if err != nil {
				slog.Default().ErrorContext(ctx, "configuration reload rejected, keeping the current configuration", "error", err)
				continue
			}
			return cfg, used, true, nil
		}
	}
}

// Run starts every component and blocks until ctx is canceled or the HTTP
// server fails. It always shuts down gracefully.
func Run(ctx context.Context, cfg config.Config, configFile string, env Env) error {
	return run(ctx, cfg, configFile, env, runOptions{})
}

// runOptions tunes a run started by a configuration reload.
type runOptions struct {
	// forceCollect collects immediately (the discovery targets changed).
	forceCollect bool
}

// targetsChanged reports whether a reload changed which images are discovered.
func targetsChanged(a, b config.DockerHubConfig) bool {
	return a.BaseURL != b.BaseURL ||
		!slices.Equal(a.Namespaces, b.Namespaces) || !slices.Equal(a.Repositories, b.Repositories) ||
		!slices.Equal(a.Include, b.Include) || !slices.Equal(a.Exclude, b.Exclude)
}

func run(ctx context.Context, cfg config.Config, configFile string, env Env, opts runOptions) (err error) {
	info := version.Get()

	tel, err := telemetry.Setup(ctx, telemetry.Options{
		ServiceName:    cfg.Telemetry.ServiceName,
		ServiceVersion: info.Version,
		Endpoint:       cfg.Telemetry.OTLPEndpoint,
		Traces:         cfg.Telemetry.Traces,
		Logs:           cfg.Telemetry.Logs,
		SampleRatio:    cfg.Telemetry.SampleRatio,
	})
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, tel.Shutdown(sctx))
	}()

	logger, err := logging.New(env.Stdout, logging.Options{
		Level: cfg.Log.Level, Format: cfg.Log.Format,
		Service: cfg.Telemetry.ServiceName, Version: info.Version, Extra: tel.LogHandler,
	})
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	logger.InfoContext(ctx, "starting",
		"go_version", info.GoVersion, "commit", info.Commit, "config_file", configFile,
		"namespaces", cfg.DockerHub.Namespaces, "repositories", cfg.DockerHub.Repositories,
		"interval", cfg.Collector.Interval.String(), "authenticated", cfg.DockerHub.Authenticated(),
		"traces", cfg.Telemetry.Traces, "otlp_logs", cfg.Telemetry.Logs)

	m := metrics.New()

	st, err := openStores(ctx, cfg.Cache, logger, m.ObserveCache)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	if err := st.Warm(ctx, time.Now().Add(-cfg.Cache.Retention)); err != nil {
		logger.WarnContext(ctx, "cache warm-up incomplete", "error", err)
	}

	hub, err := dockerhub.New(dockerhub.Options{
		BaseURL: cfg.DockerHub.BaseURL,
		HTTPClient: &http.Client{
			Timeout:   cfg.DockerHub.Timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(tel.TracerProvider)),
		},
		UserAgent:         cfg.DockerHub.UserAgent + "/" + info.Version,
		PageSize:          cfg.DockerHub.PageSize,
		MaxRetries:        cfg.DockerHub.MaxRetries,
		Username:          cfg.DockerHub.Username,
		Token:             cfg.DockerHub.Token,
		RequestsPerMinute: cfg.DockerHub.RequestsPerMinute,
		Logger:            logger,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "docker hub rate limiting",
		"requests_per_minute", dockerhub.EffectiveRPM(cfg.DockerHub.RequestsPerMinute, cfg.DockerHub.Authenticated()),
		"min_request_interval", hub.RequestInterval().String())

	concurrency := 2
	if cfg.DockerHub.Authenticated() {
		concurrency = 4
	}
	svc, err := collector.New(hub, st, collector.Options{
		Namespaces:   cfg.DockerHub.Namespaces,
		Repositories: cfg.DockerHub.Repositories,
		Include:      cfg.DockerHub.Include,
		Exclude:      cfg.DockerHub.Exclude,
		Interval:     cfg.Collector.Interval,
		Retention:    cfg.Cache.Retention,
		RunOnStart:   cfg.Collector.RunOnStart,
		ForceCollect: opts.forceCollect,
		Concurrency:  concurrency,
		Logger:       logger,
		Recorder:     m,
		Tracer:       tel.TracerProvider.Tracer("github.com/aeciopires/docker-hub-image-statistics/internal/collector"),
	})
	if err != nil {
		return err
	}
	if err := m.RegisterProvider(svc); err != nil {
		return err
	}

	handler := server.New(server.Options{
		Provider:       svc,
		Metrics:        m.Handler(),
		Observer:       m,
		Static:         web.FS(),
		Logger:         logger,
		TracerProvider: tel.TracerProvider,
		MaxHistory:     cfg.Cache.Retention,
	})
	srv := &http.Server{
		Handler:           handler,
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: min(cfg.Server.ReadTimeout, 5*time.Second),
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Server.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Address, err)
	}
	logger.InfoContext(ctx, "http server listening", "address", ln.Addr().String())
	if env.OnListen != nil {
		env.OnListen(ln.Addr().String())
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	wg.Go(func() { svc.Run(runCtx) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		logger.InfoContext(ctx, "shutdown requested", "cause", context.Cause(ctx))
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stop()

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.Server.ShutdownTimeout)
	defer cancel()
	if shutdownErr := srv.Shutdown(sctx); shutdownErr != nil {
		err = errors.Join(err, fmt.Errorf("http shutdown: %w", shutdownErr))
	}
	wg.Wait()
	logger.InfoContext(sctx, "stopped")
	return err
}

// openStores opens the enabled tiers in read order: SQLite, disk, memory.
// A tier that fails to open is skipped (logged) as long as another works.
func openStores(ctx context.Context, cfg config.CacheConfig, logger *slog.Logger, obs store.Observer) (*store.Tiered, error) {
	var (
		tiers []store.Store
		errs  []error
	)
	if cfg.SQLite.Enabled {
		s, err := store.OpenSQLite(ctx, cfg.SQLite.Path)
		if err != nil {
			logger.ErrorContext(ctx, "sqlite cache unavailable, falling back", "path", cfg.SQLite.Path, "error", err)
			errs = append(errs, err)
		} else {
			tiers = append(tiers, s)
		}
	}
	if cfg.Disk.Enabled {
		d, err := store.OpenDisk(cfg.Disk.Path)
		if err != nil {
			logger.ErrorContext(ctx, "disk cache unavailable, falling back", "path", cfg.Disk.Path, "error", err)
			errs = append(errs, err)
		} else {
			tiers = append(tiers, d)
		}
	}
	if cfg.Memory.Enabled {
		tiers = append(tiers, store.NewMemory())
	}
	if len(tiers) == 0 {
		return nil, fmt.Errorf("no cache tier could be opened: %w", errors.Join(errs...))
	}
	t, err := store.NewTiered(logger, obs, tiers...)
	if err != nil {
		return nil, err
	}
	logger.InfoContext(ctx, "cache tiers ready", "order", t.Tiers())
	return t, nil
}
