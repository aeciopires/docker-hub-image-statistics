// Package config loads the application configuration with Viper.
//
// Precedence (lowest to highest): built-in defaults, YAML file, environment
// variables (DHIS_ prefix), command-line flags.
package config

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// EnvPrefix is the prefix used by every environment variable.
const EnvPrefix = "DHIS_"

// DefaultConfigFile is looked up in the working directory when no explicit
// configuration file is provided.
const DefaultConfigFile = "config.yaml"

// Config is the root configuration object.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Log       LogConfig       `yaml:"log"`
	DockerHub DockerHubConfig `yaml:"dockerhub"`
	Collector CollectorConfig `yaml:"collector"`
	Cache     CacheConfig     `yaml:"cache"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
	Address         string        `yaml:"address"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	// Level is one of debug, info, warn or error.
	Level string `yaml:"level"`
	// Format is json (default) or text.
	Format string `yaml:"format"`
}

// DockerHubConfig configures the Docker Hub API client and discovery.
type DockerHubConfig struct {
	BaseURL string `yaml:"base_url"`
	// Namespaces are users or organizations whose public images are discovered.
	Namespaces []string `yaml:"namespaces"`
	// Repositories are explicit images ("namespace/name" or "name" for library).
	Repositories []string `yaml:"repositories"`
	// Include keeps only images matching at least one glob ("ns/name").
	Include []string `yaml:"include"`
	// Exclude drops images matching any glob ("ns/name").
	Exclude []string `yaml:"exclude"`
	// Username and Token are optional and only raise the API rate limit.
	Username   string        `yaml:"username"`
	Token      string        `yaml:"token"`
	Timeout    time.Duration `yaml:"timeout"`
	PageSize   int           `yaml:"page_size"`
	MaxRetries int           `yaml:"max_retries"`
	UserAgent  string        `yaml:"user_agent"`
	// RequestsPerMinute paces API calls. 0 selects a safe default
	// (60 anonymous, 150 authenticated). Anonymous clients may not exceed 180,
	// the Docker Hub limit per IP.
	RequestsPerMinute int `yaml:"requests_per_minute"`
}

// Docker Hub rate-limit related bounds.
const (
	// AnonymousRateLimit is Docker Hub's limit for unauthenticated API calls (per minute, per IP).
	AnonymousRateLimit = 180
	// MinAnonymousInterval is the shortest polling interval without credentials.
	MinAnonymousInterval = 5 * time.Minute
	// MinAuthenticatedInterval is the shortest polling interval with credentials.
	MinAuthenticatedInterval = time.Minute
)

// Authenticated reports whether Docker Hub credentials are configured.
func (d DockerHubConfig) Authenticated() bool { return d.Token != "" }

// CollectorConfig configures the polling scheduler.
type CollectorConfig struct {
	Interval   time.Duration `yaml:"interval"`
	RunOnStart bool          `yaml:"run_on_start"`
}

// CacheConfig configures the storage tiers (read in this order: SQLite, disk, memory).
type CacheConfig struct {
	SQLite    SQLiteConfig  `yaml:"sqlite"`
	Disk      DiskConfig    `yaml:"disk"`
	Memory    MemoryConfig  `yaml:"memory"`
	Retention time.Duration `yaml:"retention"`
}

// SQLiteConfig configures the SQLite tier.
type SQLiteConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// DiskConfig configures the JSON file tier.
type DiskConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// MemoryConfig configures the in-memory tier.
type MemoryConfig struct {
	Enabled bool `yaml:"enabled"`
}

// TelemetryConfig configures OpenTelemetry traces and logs export.
type TelemetryConfig struct {
	ServiceName string `yaml:"service_name"`
	// OTLPEndpoint is an OTLP/HTTP base URL such as http://otel-collector:4318.
	// When empty, the standard OTEL_EXPORTER_OTLP_* variables are honored.
	OTLPEndpoint string  `yaml:"otlp_endpoint"`
	Traces       bool    `yaml:"traces"`
	Logs         bool    `yaml:"logs"`
	SampleRatio  float64 `yaml:"sample_ratio"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Address:         ":8080",
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Log: LogConfig{Level: "info", Format: "json"},
		DockerHub: DockerHubConfig{
			BaseURL:    "https://hub.docker.com",
			Namespaces: []string{"library"},
			Timeout:    30 * time.Second,
			PageSize:   100,
			MaxRetries: 3,
			UserAgent:  "docker-hub-image-statistics",
		},
		Collector: CollectorConfig{Interval: time.Hour, RunOnStart: true},
		Cache: CacheConfig{
			SQLite:    SQLiteConfig{Enabled: true, Path: "data/statistics.db"},
			Disk:      DiskConfig{Enabled: true, Path: "data/statistics.json"},
			Memory:    MemoryConfig{Enabled: true},
			Retention: 90 * 24 * time.Hour,
		},
		Telemetry: TelemetryConfig{
			ServiceName: "docker-hub-image-statistics",
			SampleRatio: 1,
		},
	}
}

func (c *Config) normalize() {
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))
	c.DockerHub.BaseURL = strings.TrimRight(strings.TrimSpace(c.DockerHub.BaseURL), "/")
	c.DockerHub.Namespaces = cleanList(c.DockerHub.Namespaces)
	c.DockerHub.Repositories = cleanList(c.DockerHub.Repositories)
	c.DockerHub.Include = cleanList(c.DockerHub.Include)
	c.DockerHub.Exclude = cleanList(c.DockerHub.Exclude)
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// Validate reports every invalid setting at once.
func (c Config) Validate() error {
	var errs []error
	if c.Server.Address == "" {
		errs = append(errs, errors.New("server.address must not be empty"))
	}
	for name, d := range map[string]time.Duration{
		"server.read_timeout":     c.Server.ReadTimeout,
		"server.write_timeout":    c.Server.WriteTimeout,
		"server.idle_timeout":     c.Server.IdleTimeout,
		"server.shutdown_timeout": c.Server.ShutdownTimeout,
		"dockerhub.timeout":       c.DockerHub.Timeout,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be greater than zero", name))
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "warning", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level %q is invalid (debug, info, warn, error)", c.Log.Level))
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("log.format %q is invalid (json, text)", c.Log.Format))
	}
	if !strings.HasPrefix(c.DockerHub.BaseURL, "https://") && !strings.HasPrefix(c.DockerHub.BaseURL, "http://") {
		errs = append(errs, fmt.Errorf("dockerhub.base_url %q must be an http(s) URL", c.DockerHub.BaseURL))
	}
	if len(c.DockerHub.Namespaces) == 0 && len(c.DockerHub.Repositories) == 0 {
		errs = append(errs, errors.New("configure at least one dockerhub.namespaces or dockerhub.repositories entry"))
	}
	for _, r := range c.DockerHub.Repositories {
		if strings.Count(r, "/") > 1 {
			errs = append(errs, fmt.Errorf("dockerhub.repositories entry %q must be \"namespace/name\" or \"name\"", r))
		}
	}
	for _, p := range slices.Concat(c.DockerHub.Include, c.DockerHub.Exclude) {
		if _, err := path.Match(p, ""); err != nil {
			errs = append(errs, fmt.Errorf("invalid glob pattern %q: %w", p, err))
		}
	}
	if c.DockerHub.PageSize < 1 || c.DockerHub.PageSize > 100 {
		errs = append(errs, errors.New("dockerhub.page_size must be between 1 and 100"))
	}
	if c.DockerHub.MaxRetries < 0 {
		errs = append(errs, errors.New("dockerhub.max_retries must not be negative"))
	}
	if (c.DockerHub.Username == "") != (c.DockerHub.Token == "") {
		errs = append(errs, errors.New("dockerhub.username and dockerhub.token must be set together"))
	}
	if c.DockerHub.RequestsPerMinute < 0 {
		errs = append(errs, errors.New("dockerhub.requests_per_minute must not be negative"))
	}
	if !c.DockerHub.Authenticated() && c.DockerHub.RequestsPerMinute > AnonymousRateLimit {
		errs = append(errs, fmt.Errorf("dockerhub.requests_per_minute must not exceed %d without credentials (Docker Hub anonymous rate limit)", AnonymousRateLimit))
	}
	minInterval := MinAuthenticatedInterval
	if !c.DockerHub.Authenticated() {
		minInterval = MinAnonymousInterval
	}
	if c.Collector.Interval < minInterval {
		errs = append(errs, fmt.Errorf("collector.interval must be at least %s (Docker Hub rate limits; authenticated: %s, anonymous: %s)",
			minInterval, MinAuthenticatedInterval, MinAnonymousInterval))
	}
	if !c.Cache.SQLite.Enabled && !c.Cache.Disk.Enabled && !c.Cache.Memory.Enabled {
		errs = append(errs, errors.New("enable at least one cache tier (sqlite, disk or memory)"))
	}
	if c.Cache.SQLite.Enabled && c.Cache.SQLite.Path == "" {
		errs = append(errs, errors.New("cache.sqlite.path must not be empty"))
	}
	if c.Cache.Disk.Enabled && c.Cache.Disk.Path == "" {
		errs = append(errs, errors.New("cache.disk.path must not be empty"))
	}
	if c.Cache.Retention < 31*24*time.Hour {
		errs = append(errs, errors.New("cache.retention must be at least 744h (31 days) to compute 30d pulls"))
	}
	if c.Telemetry.SampleRatio < 0 || c.Telemetry.SampleRatio > 1 {
		errs = append(errs, errors.New("telemetry.sample_ratio must be between 0 and 1"))
	}
	if c.Telemetry.ServiceName == "" {
		errs = append(errs, errors.New("telemetry.service_name must not be empty"))
	}
	return errors.Join(errs...)
}
