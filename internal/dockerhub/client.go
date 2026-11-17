// Package dockerhub is a small client for the public Docker Hub v2 API.
package dockerhub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

// maxBodyBytes bounds every response body read from the API.
const maxBodyBytes = 8 << 20

// Docker Hub API rate limits (requests per minute, per source IP).
// Anonymous clients currently receive X-RateLimit-Limit: 180 with a 60s window.
const (
	// AnonymousRateLimit is the hard limit for unauthenticated clients.
	AnonymousRateLimit = 180
	// DefaultAnonymousRPM leaves headroom for other clients behind the same IP.
	DefaultAnonymousRPM = 60
	// DefaultAuthenticatedRPM is used when a token is configured.
	DefaultAuthenticatedRPM = 150
	// rateLimitReserve is the remaining quota at which the client pauses until reset.
	rateLimitReserve = 5
)

// Repository is the subset of the Docker Hub repository document we use.
type Repository struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	IsPrivate   bool      `json:"is_private"`
	StarCount   int64     `json:"star_count"`
	PullCount   int64     `json:"pull_count"`
	LastUpdated time.Time `json:"last_updated"`
}

// Snapshot converts the repository into a domain snapshot.
func (r Repository) Snapshot(collectedAt time.Time) stats.Snapshot {
	return stats.Snapshot{
		Namespace:   r.Namespace,
		Name:        r.Name,
		Description: r.Description,
		PullCount:   r.PullCount,
		StarCount:   r.StarCount,
		LastUpdated: r.LastUpdated,
		CollectedAt: collectedAt,
	}
}

// repositoryJSON tolerates null timestamps returned by the API.
type repositoryJSON struct {
	Namespace   string  `json:"namespace"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsPrivate   bool    `json:"is_private"`
	StarCount   int64   `json:"star_count"`
	PullCount   int64   `json:"pull_count"`
	LastUpdated *string `json:"last_updated"`
}

func (r repositoryJSON) toRepository() Repository {
	out := Repository{
		Namespace: r.Namespace, Name: r.Name, IsPrivate: r.IsPrivate,
		StarCount: r.StarCount, PullCount: r.PullCount,
	}
	if r.Description != nil {
		out.Description = *r.Description
	}
	if r.LastUpdated != nil {
		if t, err := time.Parse(time.RFC3339Nano, *r.LastUpdated); err == nil {
			out.LastUpdated = t.UTC()
		}
	}
	return out
}

type pageJSON struct {
	Count   int              `json:"count"`
	Next    *string          `json:"next"`
	Results []repositoryJSON `json:"results"`
}

// StatusError is returned for unexpected HTTP status codes.
type StatusError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("docker hub %s: unexpected status %d: %s", e.URL, e.StatusCode, e.Body)
}

// ErrNotFound is returned when a namespace or repository does not exist.
var ErrNotFound = errors.New("not found")

// PaginationLimitError reports that Docker Hub refused to page further for an
// anonymous client (only the first ~100 repositories of a namespace are
// visible without credentials). Results gathered so far remain valid.
type PaginationLimitError struct {
	Namespace string
	Fetched   int
}

func (e *PaginationLimitError) Error() string {
	return fmt.Sprintf("docker hub only lists the first %d repositories of %q to anonymous clients; "+
		"configure dockerhub.username and dockerhub.token to discover all of them", e.Fetched, e.Namespace)
}

// isPaginationLimit recognizes the 403 returned when paging too far anonymously.
func isPaginationLimit(err error) bool {
	se, ok := errors.AsType[*StatusError](err)
	return ok && se.StatusCode == http.StatusForbidden && strings.Contains(se.Body, "pagination offset too large")
}

// Options configures the client.
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
	PageSize   int
	MaxRetries int
	// Username and Token (a personal access token) are optional.
	Username string
	Token    string
	Logger   *slog.Logger
	// RequestsPerMinute paces requests client-side. Zero selects
	// DefaultAnonymousRPM or DefaultAuthenticatedRPM. Anonymous clients are
	// capped at AnonymousRateLimit.
	RequestsPerMinute int
	// Sleep and Now are injectable for tests.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

// Client talks to the Docker Hub API.
type Client struct {
	base       *url.URL
	http       *http.Client
	userAgent  string
	pageSize   int
	maxRetries int
	username   string
	token      string
	logger     *slog.Logger
	sleep      func(ctx context.Context, d time.Duration) error
	now        func() time.Time
	gap        time.Duration // minimum spacing between requests

	mu     sync.Mutex
	bearer string

	limitMu      sync.Mutex
	nextAllowed  time.Time // pacing slot
	blockedUntil time.Time // quota exhausted until this instant
}

// New validates options and returns a client.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(opts.BaseURL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("invalid docker hub base url %q", opts.BaseURL)
	}
	c := &Client{
		base:       base,
		http:       opts.HTTPClient,
		userAgent:  opts.UserAgent,
		pageSize:   opts.PageSize,
		maxRetries: max(0, opts.MaxRetries),
		username:   opts.Username,
		token:      opts.Token,
		logger:     opts.Logger,
		sleep:      opts.Sleep,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.pageSize <= 0 || c.pageSize > 100 {
		c.pageSize = 100
	}
	if c.userAgent == "" {
		c.userAgent = "docker-hub-image-statistics"
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	c.now = opts.Now
	if c.now == nil {
		c.now = time.Now
	}
	c.gap = time.Minute / time.Duration(EffectiveRPM(opts.RequestsPerMinute, opts.Token != ""))
	return c, nil
}

// EffectiveRPM resolves the requests-per-minute budget.
func EffectiveRPM(requested int, authenticated bool) int {
	switch {
	case requested <= 0 && authenticated:
		return DefaultAuthenticatedRPM
	case requested <= 0:
		return DefaultAnonymousRPM
	case !authenticated:
		return min(requested, AnonymousRateLimit)
	default:
		return requested
	}
}

// RequestInterval returns the minimum spacing between two API requests.
func (c *Client) RequestInterval() time.Duration { return c.gap }

// waitTurn blocks until the client may send the next request, honoring both
// the local pacing and an exhausted server quota.
func (c *Client) waitTurn(ctx context.Context) error {
	c.limitMu.Lock()
	now := c.now()
	at := latest(now, c.nextAllowed, c.blockedUntil)
	c.nextAllowed = at.Add(c.gap)
	c.limitMu.Unlock()

	if wait := at.Sub(now); wait > 0 {
		return c.sleep(ctx, wait)
	}
	return nil
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// observeRateLimit reads the X-RateLimit-* headers and pauses future requests
// when the remaining quota drops to the reserve.
func (c *Client) observeRateLimit(ctx context.Context, h http.Header) {
	remaining, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err != nil || remaining > rateLimitReserve {
		return
	}
	reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	until := time.Unix(reset, 0)
	c.limitMu.Lock()
	if until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
	c.limitMu.Unlock()
	c.logger.WarnContext(ctx, "docker hub rate limit almost exhausted, pausing until reset",
		"remaining", remaining, "limit", h.Get("X-RateLimit-Limit"), "reset", until.UTC())
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// ListRepositories returns every public repository of a namespace, most
// pulled first. On error it also returns the repositories fetched so far; a
// *PaginationLimitError means the anonymous listing limit was reached.
func (c *Client) ListRepositories(ctx context.Context, namespace string) ([]Repository, error) {
	next := c.endpoint("v2", "repositories", namespace)
	q := url.Values{}
	q.Set("page_size", strconv.Itoa(c.pageSize))
	// "pull_count" sorts descending: anonymous clients, limited to the first
	// page(s), still see the most relevant images.
	q.Set("ordering", "pull_count")
	next.RawQuery = q.Encode()

	var out []Repository
	for page := 1; next != nil; page++ {
		var p pageJSON
		if err := c.getJSON(ctx, next, &p); err != nil {
			if page > 1 && isPaginationLimit(err) {
				return out, &PaginationLimitError{Namespace: namespace, Fetched: len(out)}
			}
			return out, fmt.Errorf("list %s (page %d): %w", namespace, page, err)
		}
		for _, r := range p.Results {
			if r.IsPrivate {
				continue
			}
			repo := r.toRepository()
			if repo.Namespace == "" {
				repo.Namespace = namespace
			}
			out = append(out, repo)
		}
		next = nil
		if p.Next != nil && *p.Next != "" {
			u, err := c.sameOrigin(*p.Next)
			if err != nil {
				return out, err
			}
			next = u
		}
	}
	return out, nil
}

// GetRepository returns one repository. Private repositories are reported as ErrNotFound.
func (c *Client) GetRepository(ctx context.Context, namespace, name string) (Repository, error) {
	var r repositoryJSON
	u := c.endpoint("v2", "repositories", namespace, name)
	if err := c.getJSON(ctx, u, &r); err != nil {
		return Repository{}, fmt.Errorf("get %s/%s: %w", namespace, name, err)
	}
	if r.IsPrivate {
		return Repository{}, fmt.Errorf("get %s/%s: %w", namespace, name, ErrNotFound)
	}
	repo := r.toRepository()
	repo.Namespace = cmp.Or(repo.Namespace, namespace)
	repo.Name = cmp.Or(repo.Name, name)
	return repo, nil
}

// endpoint joins escaped path segments onto the base URL with a trailing slash
// (the Docker Hub API redirects when it is missing).
func (c *Client) endpoint(segments ...string) *url.URL {
	u := *c.base
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + strings.Join(escaped, "/") + "/"
	u.RawPath = ""
	return &u
}

// sameOrigin parses a pagination link and refuses foreign hosts (SSRF guard).
func (c *Client) sameOrigin(raw string) (*url.URL, error) {
	u, err := c.base.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination url: %w", err)
	}
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return nil, fmt.Errorf("refusing pagination url outside %s: %s", c.base.Host, u.Redacted())
	}
	return u, nil
}

func (c *Client) getJSON(ctx context.Context, u *url.URL, dst any) error {
	body, err := c.do(ctx, http.MethodGet, u, nil, true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// do performs a request with retries on 429/5xx and network errors.
func (c *Client) do(ctx context.Context, method string, u *url.URL, payload []byte, auth bool) ([]byte, error) {
	reauthenticated := false
	for attempt := 0; ; attempt++ {
		body, status, header, err := c.once(ctx, method, u, payload, auth)
		switch {
		case err == nil && status >= 200 && status < 300:
			return body, nil
		case err == nil && status == http.StatusNotFound:
			return nil, ErrNotFound
		case err == nil && status == http.StatusUnauthorized && auth && c.token != "" && !reauthenticated:
			// Token expired: refresh once and retry immediately.
			c.setBearer("")
			reauthenticated = true
			attempt--
			continue
		}
		if err == nil {
			err = &StatusError{StatusCode: status, URL: u.Redacted(), Body: truncate(body, 256)}
		}
		if ctx.Err() != nil || !retryable(status, err) || attempt >= c.maxRetries {
			return nil, err
		}
		wait := backoff(attempt, header)
		c.logger.WarnContext(ctx, "docker hub request failed, retrying",
			"url", u.Redacted(), "status", status, "attempt", attempt+1, "wait", wait.String(), "error", err)
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func (c *Client) once(ctx context.Context, method string, u *url.URL, payload []byte, auth bool) ([]byte, int, http.Header, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.token != "" {
		bearer, err := c.authenticate(ctx)
		if err != nil {
			return nil, 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if err := c.waitTurn(ctx); err != nil {
		return nil, 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	c.observeRateLimit(ctx, resp.Header)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return body, resp.StatusCode, resp.Header, nil
}

func (c *Client) setBearer(v string) {
	c.mu.Lock()
	c.bearer = v
	c.mu.Unlock()
}

// authenticate exchanges username + personal access token for a bearer token.
func (c *Client) authenticate(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached := c.bearer
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	payload, err := json.Marshal(map[string]string{"identifier": c.username, "secret": c.token})
	if err != nil {
		return "", err
	}
	body, err := c.do(ctx, http.MethodPost, c.endpoint("v2", "auth", "token"), payload, false)
	if err != nil {
		return "", fmt.Errorf("docker hub authentication: %w", err)
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
		return "", errors.New("docker hub authentication: missing access_token in response")
	}
	c.setBearer(resp.AccessToken)
	return resp.AccessToken, nil
}

func retryable(status int, err error) bool {
	if status == 0 {
		return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	return status == http.StatusTooManyRequests || status >= 500
}

// backoff honors Retry-After / X-RateLimit-Reset, else exponential (1s, 2s, 4s… max 60s).
func backoff(attempt int, h http.Header) time.Duration {
	const maxWait = 60 * time.Second
	if h != nil {
		if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
			return min(time.Duration(s)*time.Second, maxWait)
		}
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if d := time.Until(time.Unix(reset, 0)); d > 0 {
				return min(d, maxWait)
			}
		}
	}
	return min(time.Second<<attempt, maxWait)
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
