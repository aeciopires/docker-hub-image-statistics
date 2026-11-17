package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// newFlags returns a parsed flag set with the configuration flags.
func newFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

// isolate runs the test in an empty directory without DHIS_* variables.
func isolate(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, name := range EnvVars() {
		if _, ok := os.LookupEnv(name); ok {
			t.Setenv(name, "") // restored after the test
			os.Unsetenv(name)
		}
	}
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	isolate(t)
	for _, flags := range []*pflag.FlagSet{nil, newFlags(t)} {
		cfg, used, err := Load(flags)
		if err != nil {
			t.Fatal(err)
		}
		if used != "" {
			t.Fatalf("used = %q, want empty", used)
		}
		if cfg.Log.Level != "info" || cfg.Collector.Interval != time.Hour ||
			!slices.Equal(cfg.DockerHub.Namespaces, []string{"library"}) || cfg.Telemetry.Traces {
			t.Fatalf("unexpected defaults: %+v", cfg)
		}
	}
}

func TestLoadPicksUpDefaultFileInWorkingDir(t *testing.T) {
	isolate(t)
	if err := os.WriteFile(DefaultConfigFile, []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, used, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if used != DefaultConfigFile || cfg.Log.Level != "debug" {
		t.Fatalf("used=%q level=%q", used, cfg.Log.Level)
	}
}

func TestPrecedenceFlagsEnvFileDefaults(t *testing.T) {
	isolate(t)
	p := writeFile(t, `
server:
  address: ":9090"
log:
  level: WARN
  format: text
dockerhub:
  repositories: ["library/nginx"]
  exclude: ["grafana/*-dev"]
collector:
  interval: 30m
cache:
  retention: 1000h
`)
	t.Setenv("DHIS_LOG_LEVEL", "error")
	t.Setenv("DHIS_LOG_FORMAT", "json")
	t.Setenv("DHIS_COLLECTOR_INTERVAL", "2h")
	t.Setenv("DHIS_CACHE_RETENTION", "60d")
	t.Setenv("DHIS_CACHE_DISK_ENABLED", "false")
	t.Setenv("DHIS_TELEMETRY_TRACES", "true")
	t.Setenv("DHIS_TELEMETRY_SAMPLE_RATIO", "0.5")
	t.Setenv("DHIS_DOCKERHUB_PAGE_SIZE", "50")

	cfg, used, err := Load(newFlags(t, "-c", p, "--log-level", "debug", "-n", "acme,beta", "-i", "3h"))
	if err != nil {
		t.Fatal(err)
	}
	if used != p {
		t.Fatalf("used = %q", used)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"file only", cfg.Server.Address, ":9090"},
		{"flag wins over env and file", cfg.Log.Level, "debug"},
		{"env wins over file", cfg.Log.Format, "json"},
		{"flag duration", cfg.Collector.Interval, 3 * time.Hour},
		{"env days", cfg.Cache.Retention, 60 * 24 * time.Hour},
		{"env bool", cfg.Cache.Disk.Enabled, false},
		{"env bool true", cfg.Telemetry.Traces, true},
		{"env float", cfg.Telemetry.SampleRatio, 0.5},
		{"env int", cfg.DockerHub.PageSize, 50},
		{"default kept", cfg.Server.ShutdownTimeout, 15 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if want := []string{"acme", "beta"}; !slices.Equal(cfg.DockerHub.Namespaces, want) {
		t.Errorf("namespaces = %v, want %v (flag)", cfg.DockerHub.Namespaces, want)
	}
	if want := []string{"library/nginx"}; !slices.Equal(cfg.DockerHub.Repositories, want) {
		t.Errorf("repositories = %v", cfg.DockerHub.Repositories)
	}
}

func TestFileListsAreNormalized(t *testing.T) {
	isolate(t)
	p := writeFile(t, "dockerhub:\n  namespaces: [Grafana, grafana, \" prom \", \"\"]\n")
	cfg, _, err := Load(newFlags(t, "--config", p))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"grafana", "prom"}; !slices.Equal(cfg.DockerHub.Namespaces, want) {
		t.Fatalf("namespaces = %v, want %v", cfg.DockerHub.Namespaces, want)
	}
}

func TestLoadConfigFileFromEnv(t *testing.T) {
	isolate(t)
	p := writeFile(t, "log:\n  format: text\n")
	t.Setenv("DHIS_CONFIG_FILE", p)
	cfg, used, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if used != p || cfg.Log.Format != "text" {
		t.Fatalf("used=%q format=%q", used, cfg.Log.Format)
	}
}

func TestLoadEmptyFileKeepsDefaults(t *testing.T) {
	isolate(t)
	cfg, _, err := Load(newFlags(t, "-c", writeFile(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != ":8080" {
		t.Fatalf("address = %q", cfg.Server.Address)
	}
}

func TestListEnvSeparatorsAndNone(t *testing.T) {
	isolate(t)
	t.Setenv("DHIS_DOCKERHUB_NAMESPACES", "a, b;c d")
	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(cfg.DockerHub.Namespaces, want) {
		t.Fatalf("namespaces = %v", cfg.DockerHub.Namespaces)
	}

	t.Setenv("DHIS_DOCKERHUB_NAMESPACES", "none")
	t.Setenv("DHIS_DOCKERHUB_REPOSITORIES", "nginx")
	cfg, _, err = Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DockerHub.Namespaces) != 0 || len(cfg.DockerHub.Repositories) != 1 {
		t.Fatalf("namespaces=%v repositories=%v", cfg.DockerHub.Namespaces, cfg.DockerHub.Repositories)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		env     map[string]string
		flags   []string
		missing bool
		want    string
	}{
		{name: "missing explicit file", missing: true, want: "read config file"},
		{name: "unknown field", file: "nope: 1\n", want: "invalid keys"},
		{name: "unknown nested field", file: "server:\n  listen: x\n", want: "invalid keys"},
		{name: "bad yaml", file: "server: [\n", want: "parse config file"},
		{name: "bad bool env", env: map[string]string{"DHIS_TELEMETRY_LOGS": "maybe"}, want: "logs"},
		{name: "bad int env", env: map[string]string{"DHIS_DOCKERHUB_PAGE_SIZE": "x"}, want: "page_size"},
		{name: "bad float env", env: map[string]string{"DHIS_TELEMETRY_SAMPLE_RATIO": "x"}, want: "sample_ratio"},
		{name: "bad duration env", env: map[string]string{"DHIS_COLLECTOR_INTERVAL": "soon"}, want: "interval"},
		{name: "bad day duration env", env: map[string]string{"DHIS_CACHE_RETENTION": "xd"}, want: "retention"},
		{name: "bad duration flag", flags: []string{"-i", "never"}, want: "interval"},
		{name: "validation", env: map[string]string{"DHIS_LOG_LEVEL": "loud"}, want: "log.level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			args := tt.flags
			switch {
			case tt.missing:
				args = append(args, "-c", filepath.Join(t.TempDir(), "absent.yaml"))
			case tt.file != "":
				args = append(args, "-c", writeFile(t, tt.file))
			}
			_, _, err := Load(newFlags(t, args...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"address", func(c *Config) { c.Server.Address = "" }, "server.address"},
		{"timeout", func(c *Config) { c.Server.ReadTimeout = 0 }, "server.read_timeout"},
		{"format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
		{"base url", func(c *Config) { c.DockerHub.BaseURL = "ftp://x" }, "base_url"},
		{"no targets", func(c *Config) { c.DockerHub.Namespaces = nil }, "at least one"},
		{"repo format", func(c *Config) { c.DockerHub.Repositories = []string{"a/b/c"} }, "namespace/name"},
		{"glob", func(c *Config) { c.DockerHub.Exclude = []string{"["} }, "glob"},
		{"page size", func(c *Config) { c.DockerHub.PageSize = 101 }, "page_size"},
		{"retries", func(c *Config) { c.DockerHub.MaxRetries = -1 }, "max_retries"},
		{"credentials", func(c *Config) { c.DockerHub.Username = "u" }, "together"},
		{"interval", func(c *Config) { c.Collector.Interval = time.Second }, "collector.interval"},
		{"anonymous interval", func(c *Config) { c.Collector.Interval = 2 * time.Minute }, "at least 5m0s"},
		{"authenticated interval", func(c *Config) {
			c.DockerHub.Username, c.DockerHub.Token = "u", "t"
			c.Collector.Interval = 30 * time.Second
		}, "at least 1m0s"},
		{"negative rpm", func(c *Config) { c.DockerHub.RequestsPerMinute = -1 }, "requests_per_minute"},
		{"anonymous rpm", func(c *Config) { c.DockerHub.RequestsPerMinute = 181 }, "must not exceed 180"},
		{"no tiers", func(c *Config) {
			c.Cache.SQLite.Enabled, c.Cache.Disk.Enabled, c.Cache.Memory.Enabled = false, false, false
		}, "cache tier"},
		{"sqlite path", func(c *Config) { c.Cache.SQLite.Path = "" }, "sqlite.path"},
		{"disk path", func(c *Config) { c.Cache.Disk.Path = "" }, "disk.path"},
		{"retention", func(c *Config) { c.Cache.Retention = time.Hour }, "retention"},
		{"ratio", func(c *Config) { c.Telemetry.SampleRatio = 2 }, "sample_ratio"},
		{"service", func(c *Config) { c.Telemetry.ServiceName = "" }, "service_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestAuthenticatedLimits(t *testing.T) {
	cfg := Default()
	cfg.DockerHub.Username, cfg.DockerHub.Token = "u", "t"
	cfg.DockerHub.RequestsPerMinute = 500
	cfg.Collector.Interval = time.Minute
	if err := cfg.Validate(); err != nil {
		t.Fatalf("authenticated config must allow higher limits: %v", err)
	}
	if !cfg.DockerHub.Authenticated() || Default().DockerHub.Authenticated() {
		t.Fatal("Authenticated() mismatch")
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"1h": time.Hour, "2d": 48 * time.Hour, "0.5d": 12 * time.Hour} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestKeysAndEnvVars(t *testing.T) {
	keys := Keys()
	for _, want := range []string{"server.address", "cache.sqlite.path", "dockerhub.requests_per_minute", "telemetry.sample_ratio"} {
		if !slices.Contains(keys, want) {
			t.Errorf("Keys() missing %s", want)
		}
	}
	vars := EnvVars()
	for _, want := range []string{"DHIS_CONFIG_FILE", "DHIS_LOG_LEVEL", "DHIS_DOCKERHUB_NAMESPACES", "DHIS_CACHE_SQLITE_ENABLED"} {
		if !slices.Contains(vars, want) {
			t.Errorf("EnvVars() missing %s", want)
		}
	}
	if len(vars) != len(keys)+1 {
		t.Errorf("EnvVars has %d entries, want %d", len(vars), len(keys)+1)
	}
}

func TestEveryFlagMapsToAKey(t *testing.T) {
	keys := Keys()
	for flag, key := range flagKeys {
		if !slices.Contains(keys, key) {
			t.Errorf("flag --%s maps to unknown key %q", flag, key)
		}
	}
	fs := newFlags(t)
	for flag := range flagKeys {
		if fs.Lookup(flag) == nil {
			t.Errorf("flag --%s is mapped but not registered", flag)
		}
	}
}
