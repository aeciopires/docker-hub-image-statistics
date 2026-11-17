package dockerhub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

// fakeClock advances only when the client sleeps.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
	return nil
}

func newTestClient(t *testing.T, srv *httptest.Server, mutate func(*Options)) *Client {
	t.Helper()
	clock := &fakeClock{now: time.Now()}
	opts := Options{
		BaseURL: srv.URL, HTTPClient: srv.Client(), PageSize: 2, MaxRetries: 2,
		Sleep: clock.Sleep, Now: clock.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	for _, bad := range []string{"", "ftp://x", "http://", "://bad"} {
		if _, err := New(Options{BaseURL: bad}); err == nil {
			t.Errorf("New(%q) expected error", bad)
		}
	}
	c, err := New(Options{BaseURL: "https://hub.docker.com/", PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if c.pageSize != 100 || c.userAgent == "" || c.http == nil || c.logger == nil {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestListRepositoriesPaginatesAndSkipsPrivate(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" && r.URL.Query().Get("ordering") != "pull_count" {
			t.Errorf("ordering = %q", r.URL.Query().Get("ordering"))
		}
		if r.URL.Path != "/v2/repositories/acme/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			t.Errorf("user agent = %q", got)
		}
		switch r.URL.Query().Get("page") {
		case "":
			if r.URL.Query().Get("page_size") != "2" {
				t.Errorf("page_size = %q", r.URL.Query().Get("page_size"))
			}
			fmt.Fprintf(w, `{"count":3,"next":"%s/v2/repositories/acme/?page=2&page_size=2","results":[
				{"namespace":"acme","name":"api","pull_count":100,"star_count":2,"last_updated":"2026-11-16T09:44:08.666196Z","description":"API"},
				{"namespace":"acme","name":"secret","is_private":true,"pull_count":9}]}`, srv.URL)
		case "2":
			fmt.Fprint(w, `{"count":3,"next":null,"results":[
				{"name":"web","pull_count":50,"last_updated":null,"description":null}]}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, func(o *Options) { o.UserAgent = "test-agent" })
	repos, err := c.ListRepositories(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %+v", repos)
	}
	if repos[0].Name != "api" || repos[0].PullCount != 100 || repos[0].Description != "API" ||
		repos[0].LastUpdated.Year() != 2026 {
		t.Errorf("repo[0] = %+v", repos[0])
	}
	if repos[1].Namespace != "acme" || !repos[1].LastUpdated.IsZero() {
		t.Errorf("repo[1] = %+v", repos[1])
	}
	snap := repos[0].Snapshot(time.Unix(10, 0))
	if snap.Key() != "acme/api" || snap.PullCount != 100 || snap.CollectedAt.Unix() != 10 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestListRepositoriesRefusesForeignPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"next":"https://evil.example/steal","results":[]}`)
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv, nil).ListRepositories(t.Context(), "acme")
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"next":"http://[::1]:namedport","results":[]}`)
	}))
	defer srv2.Close()
	if _, err := newTestClient(t, srv2, nil).ListRepositories(t.Context(), "acme"); err == nil {
		t.Fatal("expected invalid url error")
	}
}

func TestGetRepository(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repositories/library/nginx/":
			fmt.Fprint(w, `{"pull_count":42,"star_count":7}`)
		case "/v2/repositories/acme/private/":
			fmt.Fprint(w, `{"name":"private","namespace":"acme","is_private":true}`)
		case "/v2/repositories/acme/broken/":
			fmt.Fprint(w, `{not json`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, nil)

	repo, err := c.GetRepository(t.Context(), "library", "nginx")
	if err != nil || repo.Namespace != "library" || repo.Name != "nginx" || repo.PullCount != 42 {
		t.Fatalf("repo = %+v, %v", repo, err)
	}
	if _, err := c.GetRepository(t.Context(), "acme", "private"); !errors.Is(err, ErrNotFound) {
		t.Errorf("private err = %v", err)
	}
	if _, err := c.GetRepository(t.Context(), "acme", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	if _, err := c.GetRepository(t.Context(), "acme", "broken"); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("broken err = %v", err)
	}
}

func TestRetriesOnRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"name":"x","namespace":"y","pull_count":1}`)
		}
	}))
	defer srv.Close()

	var waits []time.Duration
	clock := &fakeClock{now: time.Now()}
	c := newTestClient(t, srv, func(o *Options) {
		o.Now = clock.Now
		o.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return clock.Sleep(ctx, d) }
	})
	if _, err := c.GetRepository(t.Context(), "y", "x"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(waits) != 2 || waits[0] != time.Second {
		t.Fatalf("calls=%d waits=%v", calls.Load(), waits)
	}
}

func TestRetriesExhaustedAndNonRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "nope", http.StatusBadRequest)
			return
		}
		http.Error(w, strings.Repeat("x", 600), http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, nil)

	_, err := c.GetRepository(t.Context(), "a", "b")
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.StatusCode != http.StatusServiceUnavailable || !strings.HasSuffix(se.Body, "…") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(se.Error(), "503") {
		t.Errorf("error text = %q", se.Error())
	}
	_, err = c.GetRepository(t.Context(), "a", "bad")
	if se, ok := errors.AsType[*StatusError](err); !ok || se.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestSleepErrorStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	stop := errors.New("stop")
	c := newTestClient(t, srv, func(o *Options) {
		o.Sleep = func(context.Context, time.Duration) error { return stop }
	})
	if _, err := c.GetRepository(t.Context(), "a", "b"); !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
}

func TestNetworkErrorRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c, _ := New(Options{BaseURL: url, MaxRetries: 1, Sleep: noSleep})
	if _, err := c.GetRepository(t.Context(), "a", "b"); err == nil {
		t.Fatal("expected network error")
	}
}

func TestAuthentication(t *testing.T) {
	var logins, unauthorized atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/auth/token/" {
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || !strings.Contains(string(body), `"identifier":"user"`) ||
				r.Header.Get("Authorization") != "" {
				t.Errorf("bad login request: %s %s", r.Method, body)
			}
			n := logins.Add(1)
			fmt.Fprintf(w, `{"access_token":"tok%d"}`, n)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth == "Bearer tok1" && unauthorized.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized) // simulate expiry once
			return
		}
		if !strings.HasPrefix(auth, "Bearer tok") {
			t.Errorf("missing bearer: %q", auth)
		}
		fmt.Fprint(w, `{"name":"x","namespace":"y"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, func(o *Options) { o.Username, o.Token = "user", "pat" })
	if _, err := c.GetRepository(t.Context(), "y", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRepository(t.Context(), "y", "x"); err != nil {
		t.Fatal(err)
	}
	if logins.Load() != 2 {
		t.Fatalf("logins = %d, want 2 (initial + refresh)", logins.Load())
	}
}

func TestAuthenticationFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"rejected": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"empty":    func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := newTestClient(t, srv, func(o *Options) { o.Username, o.Token = "u", "p" })
			if _, err := c.GetRepository(t.Context(), "a", "b"); err == nil ||
				!strings.Contains(err.Error(), "authentication") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	h := http.Header{}
	if got := backoff(0, nil); got != time.Second {
		t.Errorf("attempt 0 = %v", got)
	}
	if got := backoff(10, h); got != 60*time.Second {
		t.Errorf("capped = %v", got)
	}
	h.Set("Retry-After", "5")
	if got := backoff(0, h); got != 5*time.Second {
		t.Errorf("retry-after = %v", got)
	}
	h = http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10))
	if got := backoff(0, h); got <= 0 || got > 31*time.Second {
		t.Errorf("rate limit reset = %v", got)
	}
}

func TestRetryable(t *testing.T) {
	if retryable(0, context.Canceled) || retryable(0, nil) || retryable(400, nil) {
		t.Error("unexpected retryable")
	}
	if !retryable(0, errors.New("dial")) || !retryable(429, nil) || !retryable(500, nil) {
		t.Error("expected retryable")
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(t.Context(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledContextNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newTestClient(t, srv, nil).GetRepository(ctx, "a", "b"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEffectiveRPM(t *testing.T) {
	cases := []struct {
		req  int
		auth bool
		want int
	}{
		{0, false, DefaultAnonymousRPM},
		{0, true, DefaultAuthenticatedRPM},
		{500, false, AnonymousRateLimit},
		{30, false, 30},
		{500, true, 500},
	}
	for _, c := range cases {
		if got := EffectiveRPM(c.req, c.auth); got != c.want {
			t.Errorf("EffectiveRPM(%d, %v) = %d, want %d", c.req, c.auth, got, c.want)
		}
	}
}

func TestPacingSpacesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"x","namespace":"y"}`)
	}))
	defer srv.Close()
	var waits []time.Duration
	clock := &fakeClock{now: time.Now()}
	c := newTestClient(t, srv, func(o *Options) {
		o.RequestsPerMinute = 30 // one request every 2s
		o.Now = clock.Now
		o.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return clock.Sleep(ctx, d) }
	})
	if c.RequestInterval() != 2*time.Second {
		t.Fatalf("interval = %v", c.RequestInterval())
	}
	for range 3 {
		if _, err := c.GetRepository(t.Context(), "y", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if len(waits) != 2 || waits[0] != 2*time.Second || waits[1] != 2*time.Second {
		t.Fatalf("waits = %v", waits)
	}
}

func TestPausesWhenQuotaAlmostExhausted(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "180")
		w.Header().Set("X-RateLimit-Remaining", "2")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clock.Now().Add(45*time.Second).Unix(), 10))
		fmt.Fprint(w, `{"name":"x","namespace":"y"}`)
	}))
	defer srv.Close()
	var waits []time.Duration
	c := newTestClient(t, srv, func(o *Options) {
		o.RequestsPerMinute = 6000
		o.Now = clock.Now
		o.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return clock.Sleep(ctx, d) }
	})
	for range 2 {
		if _, err := c.GetRepository(t.Context(), "y", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if len(waits) != 1 || waits[0] != 45*time.Second {
		t.Fatalf("waits = %v, want one 45s pause", waits)
	}
}

func TestObserveRateLimitIgnoresBadHeaders(t *testing.T) {
	c, _ := New(Options{BaseURL: "https://hub.docker.com"})
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "1")
	h.Set("X-RateLimit-Reset", "soon")
	c.observeRateLimit(t.Context(), h)
	if !c.blockedUntil.IsZero() {
		t.Fatal("invalid reset header must be ignored")
	}
}

func TestPacingHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	stop := errors.New("stop")
	c := newTestClient(t, srv, func(o *Options) {
		o.RequestsPerMinute = 1
		o.Now = time.Now
		o.Sleep = func(context.Context, time.Duration) error { return stop }
	})
	c.GetRepository(t.Context(), "a", "b") // consumes the free slot
	if _, err := c.GetRepository(t.Context(), "a", "b"); !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
}

func TestListRepositoriesAnonymousPaginationLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"pagination offset too large for anonymous requests; sign in to page further","errinfo":{}}`)
			return
		}
		fmt.Fprintf(w, `{"next":"%s/v2/repositories/acme/?page=2","results":[{"namespace":"acme","name":"a","pull_count":5}]}`, srv.URL)
	}))
	defer srv.Close()
	repos, err := newTestClient(t, srv, nil).ListRepositories(t.Context(), "acme")
	pe, ok := errors.AsType[*PaginationLimitError](err)
	if !ok || pe.Fetched != 1 || pe.Namespace != "acme" || !strings.Contains(pe.Error(), "dockerhub.token") {
		t.Fatalf("err = %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("partial results must be kept, got %d", len(repos))
	}
}

func TestListRepositoriesKeepsPartialOnError(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"next":"%s/v2/repositories/acme/?page=2","results":[{"namespace":"acme","name":"a"}]}`, srv.URL)
	}))
	defer srv.Close()
	repos, err := newTestClient(t, srv, nil).ListRepositories(t.Context(), "acme")
	if err == nil || len(repos) != 1 {
		t.Fatalf("repos=%d err=%v", len(repos), err)
	}
	if _, ok := errors.AsType[*PaginationLimitError](err); ok {
		t.Fatal("a 400 is not a pagination limit")
	}
}
