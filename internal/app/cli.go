package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v4"

	"github.com/aeciopires/docker-hub-image-statistics/internal/config"
	"github.com/aeciopires/docker-hub-image-statistics/internal/version"
)

const appName = "docker-hub-image-statistics"

// usageError marks command-line mistakes (exit code 2).
type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

// noArgs rejects positional arguments with a usage error.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if !cmd.HasParent() {
		return usageError{fmt.Errorf("unknown command %q", args[0])}
	}
	return usageError{fmt.Errorf("%q accepts no arguments, got %q", cmd.CommandPath(), args)}
}

// Main runs the CLI and returns the process exit code
// (0 success, 1 runtime error, 2 usage error).
func Main(ctx context.Context, env Env) int {
	root := newRootCommand(env)
	root.SetArgs(env.Args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	fmt.Fprintf(env.Stderr, "error: %v\n", err)
	if _, ok := errors.AsType[usageError](err); ok {
		fmt.Fprintf(env.Stderr, "Run '%s --help' for usage.\n", appName)
		return 2
	}
	return 1
}

// newRootCommand builds the command tree. The root command runs "serve", so
// "docker-hub-image-statistics -c config.yaml" works without a subcommand.
func newRootCommand(env Env) *cobra.Command {
	info := version.Get()
	root := &cobra.Command{
		Use:   appName,
		Short: "Collect and serve pull statistics of public Docker Hub images",
		Long: `Docker Hub Image Statistics discovers public Docker Hub images, polls their
pull counters on a schedule and serves total, last collection, 1d, 7d, 30d and
hourly statistics through a web UI, a JSON API and Prometheus metrics.

Configuration precedence (highest first): flags, DHIS_* environment variables,
YAML file (--config, DHIS_CONFIG_FILE or ./config.yaml), built-in defaults.
Send SIGHUP to reload the configuration without exiting.`,
		Example: `  ` + appName + `                         # serve with ./config.yaml or the defaults
  ` + appName + ` -c config.yaml -l debug  # file + log level flag
  ` + appName + ` -n myorg -r nginx -i 30m # no file: flags only
  ` + appName + ` config -c config.yaml    # print the effective configuration
  ` + appName + ` healthcheck              # probe a running instance`,
		Version:       info.Version,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), env, cmd.Flags())
		},
	}
	root.SetVersionTemplate(fmt.Sprintf("%s {{.Version}} (commit %s, built %s, %s)\n",
		appName, info.Commit, info.Date, info.GoVersion))
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	config.RegisterFlags(root.Flags())
	// Explicit flags so "-h" and "-v" are documented with these shorthands.
	root.Flags().BoolP("help", "h", false, "help for "+appName)
	root.Flags().BoolP("version", "v", false, "print the version and exit")

	root.AddCommand(
		newServeCommand(env),
		newHealthcheckCommand(env),
		newVersionCommand(env),
		newConfigCommand(env),
	)
	return root
}

func newServeCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the collector, the HTTP API and the web UI (default command)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), env, cmd.Flags())
		},
	}
	config.RegisterFlags(cmd.Flags())
	return cmd
}

func newHealthcheckCommand(env Env) *cobra.Command {
	var (
		target  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe a running instance (container HEALTHCHECK, no shell or curl needed)",
		Long: `Requests the health endpoint and exits 0 when it answers 200 OK.
The default URL uses the port of DHIS_SERVER_ADDRESS (default 8080).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return healthcheck(cmd.Context(), env, target, timeout)
		},
	}
	cmd.Flags().StringVarP(&target, "url", "u", defaultHealthURL(), "health endpoint to probe")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 3*time.Second, "request timeout")
	return cmd
}

func defaultHealthURL() string {
	if addr := os.Getenv(config.EnvName("server.address")); addr != "" {
		if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
			return "http://127.0.0.1:" + port + "/healthz"
		}
	}
	return "http://127.0.0.1:8080/healthz"
}

func healthcheck(ctx context.Context, env Env, target string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	fmt.Fprintln(env.Stdout, "ok")
	return nil
}

func newVersionCommand(env Env) *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  noArgs,
		Run: func(*cobra.Command, []string) {
			info := version.Get()
			if short {
				fmt.Fprintln(env.Stdout, info.Version)
				return
			}
			fmt.Fprintf(env.Stdout, "%s %s (commit %s, built %s, %s)\n",
				appName, info.Version, info.Commit, info.Date, info.GoVersion)
		},
	}
	cmd.Flags().BoolVarP(&short, "short", "s", false, "print only the version number")
	return cmd
}

func newConfigCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate and print the effective configuration (secrets redacted)",
		Long: `Loads the configuration exactly like "serve" (defaults, YAML file, DHIS_*
variables and flags), validates it and prints the result as YAML. Exits with an
error listing every problem when the configuration is invalid.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printConfig(env, cmd.Flags())
		},
	}
	config.RegisterFlags(cmd.Flags())
	return cmd
}

func printConfig(env Env, flags *pflag.FlagSet) error {
	cfg, used, err := config.Load(flags)
	if err != nil {
		return err
	}
	if cfg.DockerHub.Token != "" {
		cfg.DockerHub.Token = "<redacted>"
	}
	source := used
	if source == "" {
		source = "none (defaults, environment and flags)"
	}
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "# configuration file: %s\n%s", source, out.String())
	return nil
}
