package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/config"
)

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func fakeHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repositories/acme/":
			fmt.Fprint(w, `{"next":null,"results":[{"namespace":"acme","name":"api","pull_count":1234,"star_count":3}]}`)
		case "/v2/repositories/library/nginx/":
			fmt.Fprint(w, `{"namespace":"library","name":"nginx","pull_count":99999}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestServeEndToEnd(t *testing.T) {
	hub := fakeHub(t)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(fmt.Sprintf(`
server:
  address: "127.0.0.1:0"
dockerhub:
  base_url: %q
  namespaces: [acme]
  repositories: [nginx]
  requests_per_minute: 180
cache:
  sqlite: {path: %q}
  disk: {path: %q}
`, hub.URL, filepath.Join(dir, "s.db"), filepath.Join(dir, "s.json"))), 0o600)

	t.Setenv("DHIS_LOG_LEVEL", "debug")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	addrCh := make(chan string, 1)
	stdout := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, Env{
			Args: []string{"serve", "--config", cfgFile}, Stdout: stdout, Stderr: io.Discard,
			OnListen: func(a string) { addrCh <- a },
		})
	}()
	var addr string
	select {
	case addr = <-addrCh:
	case code := <-done:
		t.Fatalf("exited early with %d: %s", code, stdout.String())
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	base := "http://" + addr

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := get(t, base+"/api/v1/images")
		if strings.Contains(body, `"count":2`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("images never collected: %s\nlogs: %s", body, stdout.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	for path, want := range map[string]string{
		"/healthz":                             "ok",
		"/readyz":                              "ready",
		"/metrics":                             `dockerhub_image_pulls_total{namespace="acme",repository="api"} 1234`,
		"/api/v1/summary":                      `"pulls_total":101233`,
		"/api/v1/images/library/nginx/history": `"points"`,
		"/":                                    "Docker Hub Image Statistics",
	} {
		if code, body := get(t, base+path); code != 200 || !strings.Contains(body, want) {
			t.Errorf("%s = %d, missing %q", path, code, want)
		}
	}

	// healthcheck subcommand against the running server.
	var hcOut bytes.Buffer
	_, port, _ := net.SplitHostPort(addr)
	t.Setenv("DHIS_SERVER_ADDRESS", ":"+port)
	if code := Main(t.Context(), Env{
		Args: []string{"healthcheck"}, Stdout: &hcOut, Stderr: io.Discard,
	}); code != 0 || !strings.Contains(hcOut.String(), "ok") {
		t.Errorf("healthcheck = %d %q", code, hcOut.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d\n%s", code, stdout.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
	logs := stdout.String()
	for _, want := range []string{`"level":"INFO"`, `"msg":"starting"`, `"msg":"stopped"`, `"msg":"collection finished"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %s", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "s.db")); err != nil {
		t.Errorf("sqlite file not created: %v", err)
	}
}

func TestCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := []struct {
		name     string
		args     []string
		code     int
		stdout   string
		stderr   string
		noStdout string
	}{
		{name: "version command", args: []string{"version"}, stdout: "docker-hub-image-statistics dev (commit"},
		{name: "version short", args: []string{"version", "-s"}, stdout: "dev\n"},
		{name: "-v", args: []string{"-v"}, stdout: "docker-hub-image-statistics dev"},
		{name: "--version", args: []string{"--version"}, stdout: "(commit unknown"},
		{name: "-h", args: []string{"-h"}, stdout: "-c, --config"},
		{name: "--help", args: []string{"--help"}, stdout: "Available Commands:"},
		{name: "help command", args: []string{"help", "serve"}, stdout: "-n, --namespaces"},
		{name: "serve -h", args: []string{"serve", "-h"}, stdout: "-l, --log-level"},
		{name: "healthcheck -h", args: []string{"healthcheck", "--help"}, stdout: "-u, --url", noStdout: "--config"},
		{name: "unknown command", args: []string{"bogus"}, code: 2, stderr: `unknown command "bogus"`},
		{name: "unknown flag", args: []string{"serve", "--unknown-flag"}, code: 2, stderr: "unknown flag"},
		{name: "unexpected argument", args: []string{"version", "extra"}, code: 2, stderr: "accepts no arguments"},
		{name: "missing config", args: []string{"-c", filepath.Join(t.TempDir(), "missing.yaml")}, code: 1, stderr: "read config file"},
		{name: "invalid flag value", args: []string{"config", "-l", "loud"}, code: 1, stderr: "log.level"},
		{name: "config defaults", args: []string{"config"}, stdout: "# configuration file: none"},
		{name: "config flags", args: []string{"config", "-n", "acme", "-r", "nginx", "-i", "2h", "-a", ":9000"},
			stdout: "address: :9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Main(t.Context(), Env{Args: tc.args, Stdout: &out, Stderr: &errOut})
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, tc.code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), tc.stdout) {
				t.Errorf("stdout missing %q:\n%s", tc.stdout, out.String())
			}
			if !strings.Contains(errOut.String(), tc.stderr) {
				t.Errorf("stderr missing %q:\n%s", tc.stderr, errOut.String())
			}
			if tc.noStdout != "" && strings.Contains(out.String(), tc.noStdout) {
				t.Errorf("stdout must not contain %q", tc.noStdout)
			}
		})
	}
}

func TestConfigCommandRedactsToken(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DHIS_DOCKERHUB_USERNAME", "me")
	t.Setenv("DHIS_DOCKERHUB_TOKEN", "dckr_pat_secret")
	var out bytes.Buffer
	if code := Main(t.Context(), Env{Args: []string{"config", "-l", "debug"}, Stdout: &out, Stderr: io.Discard}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(out.String(), "dckr_pat_secret") || !strings.Contains(out.String(), "<redacted>") {
		t.Fatalf("token not redacted:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "level: debug") {
		t.Fatalf("flag not applied:\n%s", out.String())
	}
}

func TestHealthcheckFailures(t *testing.T) {
	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()
	env := Env{Stdout: io.Discard, Stderr: io.Discard}
	for _, args := range [][]string{
		{"healthcheck", "-u", unhealthy.URL},
		{"healthcheck", "--url", "http://127.0.0.1:1/healthz", "-t", "200ms"},
		{"healthcheck", "--url", "://bad"},
		{"healthcheck", "--nope"},
	} {
		env.Args = args
		if Main(t.Context(), env) == 0 {
			t.Errorf("%v must fail", args)
		}
	}
}

func TestDefaultHealthURL(t *testing.T) {
	t.Setenv("DHIS_SERVER_ADDRESS", "0.0.0.0:9123")
	if got := defaultHealthURL(); got != "http://127.0.0.1:9123/healthz" {
		t.Fatalf("url = %q", got)
	}
	t.Setenv("DHIS_SERVER_ADDRESS", "bad")
	if got := defaultHealthURL(); got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("url = %q", got)
	}
}

func baseConfig(t *testing.T) config.Config {
	cfg := config.Default()
	cfg.Server.Address = "127.0.0.1:0"
	cfg.Cache.SQLite.Path = filepath.Join(t.TempDir(), "s.db")
	cfg.Cache.Disk.Path = filepath.Join(t.TempDir(), "s.json")
	cfg.Collector.RunOnStart = false
	return cfg
}

func TestRunErrors(t *testing.T) {
	env := Env{Stdout: io.Discard, Stderr: io.Discard}

	cfg := baseConfig(t)
	cfg.Telemetry.Traces = true
	cfg.Telemetry.OTLPEndpoint = "not-a-url"
	if err := Run(t.Context(), cfg, "", env); err == nil {
		t.Error("expected telemetry error")
	}

	cfg = baseConfig(t)
	cfg.Log.Level = "loud"
	if err := Run(t.Context(), cfg, "", env); err == nil {
		t.Error("expected logger error")
	}

	cfg = baseConfig(t)
	cfg.DockerHub.BaseURL = "::"
	if err := Run(t.Context(), cfg, "", env); err == nil {
		t.Error("expected docker hub client error")
	}

	cfg = baseConfig(t)
	cfg.DockerHub.Namespaces = nil
	if err := Run(t.Context(), cfg, "", env); err == nil {
		t.Error("expected collector error")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg = baseConfig(t)
	cfg.Server.Address = ln.Addr().String()
	if err := Run(t.Context(), cfg, "", env); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("expected listen error, got %v", err)
	}
}

func TestOpenStoresFallbacks(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)

	cfg := config.CacheConfig{
		SQLite: config.SQLiteConfig{Enabled: true, Path: filepath.Join(blocker, "x", "s.db")},
		Disk:   config.DiskConfig{Enabled: true, Path: filepath.Join(blocker, "x", "s.json")},
		Memory: config.MemoryConfig{Enabled: true},
	}
	st, err := openStores(t.Context(), cfg, logger, nil)
	if err != nil {
		t.Fatalf("memory fallback must work: %v", err)
	}
	if got := st.Tiers(); len(got) != 1 || got[0] != "memory" {
		t.Fatalf("tiers = %v", got)
	}

	cfg.Memory.Enabled = false
	if _, err := openStores(t.Context(), cfg, logger, nil); err == nil {
		t.Fatal("expected error when no tier opens")
	}
}

func TestServeReloadsConfiguration(t *testing.T) {
	hub := fakeHub(t)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeCfg := func(body string) {
		t.Helper()
		base := fmt.Sprintf("server:\n  address: \"127.0.0.1:0\"\ncache:\n  sqlite: {path: %q}\n  disk: {path: %q}\n",
			filepath.Join(dir, "s.db"), filepath.Join(dir, "s.json"))
		if err := os.WriteFile(cfgFile, []byte(base+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(fmt.Sprintf("dockerhub:\n  base_url: %q\n  namespaces: [acme]\n  repositories: [nginx]\n  requests_per_minute: 180\n", hub.URL))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	addrCh := make(chan string, 4)
	reload := make(chan struct{})
	stdout := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, Env{
			Args: []string{"serve", "-c", cfgFile}, Stdout: stdout, Stderr: io.Discard,
			OnListen: func(a string) { addrCh <- a }, Reload: reload,
		})
	}()

	waitAddr := func() string {
		t.Helper()
		select {
		case a := <-addrCh:
			return "http://" + a
		case <-time.After(10 * time.Second):
			t.Fatalf("server did not (re)start: %s", stdout.String())
		}
		return ""
	}
	waitCount := func(base, want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, body := get(t, base+"/api/v1/images"); strings.Contains(body, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("never got %s\nlogs: %s", want, stdout.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	base := waitAddr()
	waitCount(base, `"count":2`)

	// Invalid configuration: rejected, the current run keeps serving.
	writeCfg("log:\n  level: loud\n")
	reload <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "configuration reload rejected") {
		if time.Now().After(deadline) {
			t.Fatalf("reload was not rejected: %s", stdout.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _ := get(t, base+"/healthz"); code != 200 {
		t.Fatalf("server must keep running after a rejected reload, got %d", code)
	}

	// Valid configuration: only nginx is tracked after the reload.
	writeCfg(fmt.Sprintf("dockerhub:\n  base_url: %q\n  namespaces: []\n  repositories: [nginx]\n  requests_per_minute: 180\n", hub.URL))
	reload <- struct{}{}
	base = waitAddr()
	waitCount(base, `"count":1`)
	if !strings.Contains(stdout.String(), `"cause":"configuration reload"`) ||
		!strings.Contains(stdout.String(), "configuration reloaded") {
		t.Errorf("reload not logged: %s", stdout.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
}
