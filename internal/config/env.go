package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration extends time.ParseDuration with a "d" (24h) suffix, e.g. "90d".
func ParseDuration(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(v)
}

// splitList splits list values given as one string (environment variables):
// commas, spaces and semicolons separate items and "none" means an empty list.
func splitList(v string) []string {
	if strings.EqualFold(strings.TrimSpace(v), "none") {
		return []string{}
	}
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
}

// EnvName returns the environment variable of a configuration key
// ("log.level" -> "DHIS_LOG_LEVEL").
func EnvName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// EnvVars returns the full names of every supported environment variable.
func EnvVars() []string {
	keys := Keys()
	names := make([]string, 0, len(keys)+1)
	names = append(names, EnvPrefix+"CONFIG_FILE")
	for _, k := range keys {
		names = append(names, EnvName(k))
	}
	return names
}
