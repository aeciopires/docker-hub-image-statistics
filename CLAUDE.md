# CLAUDE.md

Guidance for Claude Code (and other AI assistants) working in this repository.

<!-- TOC -->
- [Project summary](#project-summary)
- [Go: always use the Modern Go Guidelines](#go-always-use-the-modern-go-guidelines)
- [Commands](#commands)
- [Code map](#code-map)
- [Conventions](#conventions)
- [Quality gates](#quality-gates)
- [Versioning](#versioning)
- [Gotchas](#gotchas)
<!-- TOC -->

## Project summary

Go 1.27 service that discovers public Docker Hub images, polls their pull
counters on a schedule (default 1h), stores snapshots in cache tiers
(SQLite → disk → memory) and serves total/1d/7d/30d/hourly statistics through a
Material Design 3 web UI, a JSON API, Prometheus metrics, OpenTelemetry traces
and JSON logs. Ships a distroless image, a Helm 4 chart and docker compose stacks.

## Go: always use the Modern Go Guidelines

**Whenever you create, analyze, review, refactor or test Go code**, use the
`modern-go-guidelines` skill (plugin `modern-go-guidelines@goland-claude-marketplace`,
source: <https://github.com/JetBrains/go-modern-guidelines>):

1. Invoke the skill `modern-go-guidelines:use-modern-go`.
2. Run its `list` subcommand for the file or Go version (`list --go-version 1.27`)
   and read the **whole** output (do not truncate it).
3. Call `explain <id>` for any guideline you are about to apply or skip.
4. Treat the guidelines as authoritative unless they would not compile or would
   change behavior.

Guidelines already adopted here include: `encoding/json/v2` for JSON,
`sync.WaitGroup.Go`, `t.Context()`, `errors.AsType`, `cmp.Or`, `maps.Keys` +
`slices.Sorted`, `slices.SortedFunc`, range-over-int, `min`/`max`, `clear`,
`strings.CutSuffix`, method-aware `ServeMux` patterns with `r.PathValue`,
`slog.NewMultiHandler` and `context.WithCancelCause` (configuration reload).

If the skill is not installed:
`/plugin marketplace add JetBrains/go-modern-guidelines` and install
`modern-go-guidelines`.

## Commands

```bash
make help            # all targets
make test            # unit tests
make coverage        # tests with the 90% coverage gate
make lint            # golangci-lint (v2 config in .golangci.yml)
make check           # every gate: fmt, vet, lint, coverage, govulncheck, version, hadolint, helm
make run             # run locally (CONFIG=config.yaml when present)
make reload          # SIGHUP the app started by make run (config reload)
make docker-build docker-scan
make up / make down              # app only (compose); make compose-reload = SIGHUP
make kind-up / make kind-delete  # kind cluster + image + chart (deploy/kind/values.yaml)
make helm-install / helm-reload / helm-uninstall / helm-status
make stack-up / make stack-down  # app + observability stack
make helm-validate helm-docs
make docs-validate               # render all Mermaid diagrams with mermaid-cli (Docker)
make dashboards                  # regenerate Grafana JSON from scripts/gen-dashboards.py
```

Tools are pinned in `.mise.toml`; the Makefile calls them through `mise exec --`.

## Code map

| Path | Purpose |
| --- | --- |
| `cmd/docker-hub-image-statistics` | `main`: signals → `app.Main` |
| `internal/app` | Wiring, Cobra CLI in `cli.go` (`serve`, `config`, `healthcheck`, `version`, `completion`), reload loop, graceful shutdown |
| `internal/config` | Viper loader (`loader.go`): defaults → YAML → `DHIS_*` → flags; `UnmarshalExact` + `Validate()` |
| `internal/dockerhub` | API client: pagination (ordered by pulls), retries, rate limiting, auth |
| `internal/collector` | Discovery, globs, schedule, status, atomic stats view |
| `internal/stats` | Window (1d/7d/30d) and hourly series math |
| `internal/store` | `Store` interface; `SQLite`, `Disk`, `Memory`, `Tiered` |
| `internal/metrics` | Prometheus registry; image metrics read from the collector on scrape |
| `internal/server` | Routes, middlewares (security headers/CSP, recover, metrics, otelhttp, logging) |
| `internal/telemetry` | OTLP/HTTP traces + logs |
| `internal/logging` | slog JSON with `trace_id`/`span_id` |
| `internal/web/static` | Embedded UI (no external resources allowed) |
| `charts/docker-hub-image-statistics` | Helm 4 chart + Grafana dashboards (`dashboards/`) |
| `deploy/compose` | `compose.yaml` (app) and `compose.observability.yaml` (stack) + configs |
| `scripts/` | deps check/install, version sync, Mermaid validation, dashboard generator |

## Conventions

- Every Markdown file has a TOC at the top. Docs target beginners (analogies +
  Mermaid diagrams) and are in English.
- Logs: JSON, default level INFO; probes/scrapes/static files log at DEBUG.
- UI: Material Design 3 tokens, dark theme default, `textContent` only, no
  external URLs (enforced by `TestNoExternalResources` and the CSP).
- Helm: Helm 4 only (`dhis.requireHelm4`), document values with `# --` for
  helm-docs, keep restricted security defaults, `extraObjects` for extra manifests.
- Commits: Conventional Commits. Do not commit unless asked.

## Quality gates

- Total coverage of `./internal/...` must stay **≥ 90%** (`make coverage`).
- `golangci-lint`, `govulncheck`, `hadolint`, `trivy` (image, fs, chart) must be clean.
- Mermaid diagrams must render (`make docs-validate`).

## Versioning

`VERSION` is the single source of truth (SemVer). `make version-sync` updates
`Chart.yaml` (`version`, `appVersion`) and the chart README;
`make version-check` fails when the chart or `CHANGELOG.md` is out of sync. The
Makefile injects the version into the binary (`-ldflags`) and the image tags.

## Gotchas

- Docker Hub anonymous limits: 180 requests/min per IP and only the first 100
  repositories of a namespace (403 "pagination offset too large"). The client
  orders by `pull_count` and reports a *warning*, not an error.
- `ordering=pull_count` sorts **descending** on Docker Hub.
- Jaeger v2.23 removed the HTTP API used by Grafana's Jaeger data source; Grafana
  uses Tempo, Jaeger is used via its own UI.
- Tempo 3 removed `metrics_generator.traces_storage` (and the `local-blocks` processor).
- `select` with an already-canceled context and a free semaphore picks randomly:
  check `ctx.Err()` first (see `collector.discover`).
- CLI: Cobra + pflag (POSIX flags). Add a config flag by registering it in
  `config.RegisterFlags` and mapping it in `flagKeys` (a test checks both).
  Flags have no default of their own so unset flags never override env/file.
- Viper only reads environment variables for keys that have a default: every
  leaf of `config.Default()` is registered automatically by `walk`.
- Reload = SIGHUP: `app.serve` loads + validates the new config, stops the
  current run (`context.WithCancelCause(ErrReload)`) and starts a new one.
  Collector `firstRun` keeps the schedule if the cache is recent, unless a
  target changed/has no cached data (otherwise "last collection" pulls drop to 0
  and the rate limit is wasted).
- `pkill -f` patterns must not appear literally in the recipe that runs them
  (`make reload` uses `[b]in/...` and `pgrep -u`).
- macOS uses Colima: only `$HOME` is shared with the VM, so never bind-mount
  `mktemp` paths (`/var/folders`); scripts use `$ROOT/.tmp/`.
- macOS: bash 3.2 and GNU make 3.81 — no `sed -i`, no make 4 features.
