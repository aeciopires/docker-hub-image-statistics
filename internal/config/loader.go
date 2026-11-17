package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// FlagConfig is the name of the flag that selects the configuration file.
const FlagConfig = "config"

// flagKeys maps command-line flags to configuration keys. Flags win over the
// environment, which wins over the file, which wins over the defaults.
var flagKeys = map[string]string{
	"address":       "server.address",
	"log-level":     "log.level",
	"log-format":    "log.format",
	"namespaces":    "dockerhub.namespaces",
	"repositories":  "dockerhub.repositories",
	"interval":      "collector.interval",
	"otlp-endpoint": "telemetry.otlp_endpoint",
	"traces":        "telemetry.traces",
	"otlp-logs":     "telemetry.logs",
}

// RegisterFlags adds the configuration flags to fs. Flags have no default of
// their own: unset flags never override the environment or the file.
func RegisterFlags(fs *pflag.FlagSet) {
	d := Default()
	fs.StringP(FlagConfig, "c", "", "YAML configuration file (env: DHIS_CONFIG_FILE, default ./config.yaml when present)")
	fs.StringP("address", "a", "", fmt.Sprintf("HTTP listen address (default %q)", d.Server.Address))
	fs.StringP("log-level", "l", "", "log level: debug, info, warn, error (default \"info\")")
	fs.String("log-format", "", "log format: json, text (default \"json\")")
	fs.StringSliceP("namespaces", "n", nil, "Docker Hub users/organizations to discover, comma separated (default \"library\")")
	fs.StringSliceP("repositories", "r", nil, "explicit images (\"ns/name\" or \"name\"), comma separated")
	fs.StringP("interval", "i", "", "collection interval, e.g. 30m, 1h, 1d (default \"1h\")")
	fs.String("otlp-endpoint", "", "OTLP/HTTP endpoint, e.g. http://localhost:4318")
	fs.Bool("traces", false, "export OpenTelemetry traces")
	fs.Bool("otlp-logs", false, "export logs over OTLP")
}

// Keys returns every configuration key ("server.address", "log.level", ...).
func Keys() []string {
	var keys []string
	walk(reflect.ValueOf(Default()), "", func(key string, _ reflect.Value) { keys = append(keys, key) })
	return keys
}

// walk visits every leaf field of a struct, naming it by its yaml tags.
func walk(v reflect.Value, prefix string, visit func(key string, field reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		f := v.Field(i)
		if f.Kind() == reflect.Struct && f.Type() != reflect.TypeFor[time.Duration]() {
			walk(f, key, visit)
			continue
		}
		visit(key, f)
	}
}

// Load builds the configuration. The file is the --config flag, otherwise
// DHIS_CONFIG_FILE, otherwise ./config.yaml when it exists. flags may be nil.
// It returns the configuration and the file actually read ("" for none).
func Load(flags *pflag.FlagSet) (Config, string, error) {
	v := viper.NewWithOptions(
		viper.KeyDelimiter("."),
		viper.EnvKeyReplacer(strings.NewReplacer(".", "_")),
	)
	v.SetEnvPrefix(strings.TrimSuffix(EnvPrefix, "_"))
	v.AutomaticEnv() // only keys with a default are looked up, so set them all
	walk(reflect.ValueOf(Default()), "", func(key string, f reflect.Value) { v.SetDefault(key, f.Interface()) })

	explicit := ""
	if flags != nil {
		for name, key := range flagKeys {
			if f := flags.Lookup(name); f != nil {
				if err := v.BindPFlag(key, f); err != nil {
					return Config{}, "", err
				}
			}
		}
		if f := flags.Lookup(FlagConfig); f != nil {
			explicit = f.Value.String()
		}
	}

	file, required := explicit, explicit != ""
	if file == "" {
		file = os.Getenv(EnvPrefix + "CONFIG_FILE")
		required = file != ""
	}
	if file == "" {
		file = DefaultConfigFile
	}

	used := ""
	switch _, err := os.Stat(file); { //nolint:gosec // the path is operator configuration (flag/env), not user input
	case err == nil:
		v.SetConfigFile(file)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err != nil {
			return Config{}, "", fmt.Errorf("parse config file %q: %w", file, err)
		}
		used = file
	case errors.Is(err, fs.ErrNotExist) && !required:
		// No configuration file: defaults, environment and flags only.
	default:
		return Config{}, "", fmt.Errorf("read config file %q: %w", file, err)
	}

	var cfg Config
	// UnmarshalExact rejects unknown keys, catching typos in the file.
	if err := v.UnmarshalExact(&cfg, func(dc *mapstructure.DecoderConfig) {
		dc.TagName = "yaml"
		dc.WeaklyTypedInput = true
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(durationHook, listHook)
	}); err != nil {
		return Config{}, "", fmt.Errorf("invalid configuration (check the file, DHIS_* variables and flags): %w", err)
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, "", err
	}
	return cfg, used, nil
}

// durationHook decodes strings such as "30m" or "90d" into time.Duration.
func durationHook(from, to reflect.Type, data any) (any, error) {
	if to != reflect.TypeFor[time.Duration]() || from.Kind() != reflect.String {
		return data, nil
	}
	return ParseDuration(strings.TrimSpace(data.(string)))
}

// listHook decodes a single string (environment variable) into a list.
func listHook(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Slice || to.Elem().Kind() != reflect.String {
		return data, nil
	}
	return splitList(data.(string)), nil
}
