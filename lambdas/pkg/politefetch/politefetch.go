// Package politefetch is the only allowed way to make outbound HTTP requests
// against external sites from this project. Per
// .ralph/specs/06-discovery-and-compliance.md and lambdas/CLAUDE.md rule #4,
// every fetch goes through Client.Fetch which:
//
//  1. Reads cached robots.txt (24h TTL) and refuses if disallowed for our UA.
//  2. Honours per-host throttle (default 1 req per 5s, fleet-wide via
//     DynamoDB).
//  3. Sends a documented User-Agent.
//  4. Retries 429/5xx with exponential backoff + jitter (default 3 retries).
//  5. Honours an ETag cache (24h TTL) — 304 responses return the cached body.
//
// The bot URL/contact in the User-Agent must be a real, working URL so site
// owners can ask us to stop. Default points at outreach.<base>/bot.
package politefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultUserAgent is used when Config.UserAgent is empty. The bot URL is the
// project's contact page; site owners email there to demand removal.
const DefaultUserAgent = "WebsiteAgencyBot/1.0 (+https://outreach.agency.techar.ch/bot)"

// DefaultThrottle is the per-host floor between successive fetches. The spec
// says 1 per 5 seconds; tests can override via Config.Throttle.
const DefaultThrottle = 5 * time.Second

// DefaultMaxRetries is the per-call retry budget for 429/5xx responses.
const DefaultMaxRetries = 3

// DefaultHTTPTimeout caps any single request. Lambdas that need longer
// timeouts pass their own *http.Client via Config.HTTP.
const DefaultHTTPTimeout = 30 * time.Second

// ErrDisallowed is returned by Fetch when robots.txt forbids the URL for our
// User-Agent (or for *).
var ErrDisallowed = errors.New("politefetch: disallowed by robots.txt")

// ErrThrottleTimeout is returned when the caller's context expires while
// waiting for the per-host throttle window to open.
var ErrThrottleTimeout = errors.New("politefetch: throttle wait deadline exceeded")

// Config tunes a Client.
type Config struct {
	UserAgent  string        // default DefaultUserAgent
	Throttle   time.Duration // default DefaultThrottle (per host)
	MaxRetries int           // default DefaultMaxRetries
	HTTP       *http.Client  // default http.Client{Timeout: 30s}
	Now        func() time.Time
	// SleepFn is called to wait before retrying or for throttle delays.
	// Tests inject a no-op so they don't actually sleep.
	SleepFn func(ctx context.Context, d time.Duration) error
}

// Client is the polite-fetch entry point. Construct via New.
type Client struct {
	cfg      Config
	robots   *robotsCache
	throttle *hostThrottle
	etag     *etagCache
}

// Response is the shape returned by Client.Fetch.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	URL        string
	// FromCache is true when the response body came from the ETag cache
	// (i.e. the upstream returned 304 Not Modified).
	FromCache bool
}

// New returns a Client with sensible defaults. Override with Config fields.
func New(cfg Config) *Client {
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Throttle <= 0 {
		cfg.Throttle = DefaultThrottle
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.SleepFn == nil {
		cfg.SleepFn = sleepWithCtx
	}
	return &Client{
		cfg:      cfg,
		robots:   &robotsCache{userAgent: cfg.UserAgent, http: cfg.HTTP, now: cfg.Now},
		throttle: &hostThrottle{floor: cfg.Throttle, now: cfg.Now, sleep: cfg.SleepFn},
		etag:     &etagCache{now: cfg.Now},
	}
}

// Fetch fetches urlStr respecting robots, the per-host throttle, the ETag
// cache, and retry/backoff on 429/5xx. The User-Agent header is set from
// Config.UserAgent. Successful (2xx, 304) responses are returned with the
// body filled; 4xx (except 429) responses are returned as-is so callers can
// see them; 5xx exhaust the retry budget then surface as the last response.
func (c *Client) Fetch(ctx context.Context, urlStr string) (*Response, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("politefetch: parsing URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("politefetch: unsupported scheme %q", u.Scheme)
	}

	allowed, crawlDelay, err := c.robots.Allowed(ctx, u)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDisallowed
	}

	// Per-host throttle — honour Crawl-delay if it's stricter than ours.
	floor := c.cfg.Throttle
	if crawlDelay > floor {
		floor = crawlDelay
	}
	if err := c.throttle.Wait(ctx, u.Host, floor); err != nil {
		return nil, err
	}

	// ETag conditional request, if we have one cached.
	cached, ok := c.etag.Get(ctx, urlStr)

	body, statusCode, header, fromCache, err := c.fetchWithRetry(ctx, urlStr, cached)
	if err != nil {
		return nil, err
	}

	resp := &Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       body,
		URL:        urlStr,
		FromCache:  fromCache,
	}

	// Refresh the ETag cache on success or 304 (cache hit reuses body).
	if !fromCache && statusCode >= 200 && statusCode < 300 {
		c.etag.Put(ctx, urlStr, header, body)
	}

	_ = ok // ok was only useful to feed cached above
	return resp, nil
}

// fetchWithRetry runs the HTTP request with exponential backoff + jitter
// retries on 429/5xx. cached is the previously-stored ETag entry, if any.
func (c *Client) fetchWithRetry(ctx context.Context, urlStr string, cached *etagEntry) (body []byte, status int, header http.Header, fromCache bool, err error) {
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if reqErr != nil {
			return nil, 0, nil, false, fmt.Errorf("politefetch: building request: %w", reqErr)
		}
		req.Header.Set("User-Agent", c.cfg.UserAgent)
		if cached != nil {
			if cached.ETag != "" {
				req.Header.Set("If-None-Match", cached.ETag)
			}
			if cached.LastModified != "" {
				req.Header.Set("If-Modified-Since", cached.LastModified)
			}
		}

		resp, doErr := c.cfg.HTTP.Do(req)
		if doErr != nil {
			if attempt < c.cfg.MaxRetries {
				if waitErr := c.cfg.SleepFn(ctx, backoffDuration(attempt)); waitErr != nil {
					return nil, 0, nil, false, waitErr
				}
				continue
			}
			return nil, 0, nil, false, fmt.Errorf("politefetch: HTTP do: %w", doErr)
		}

		if resp.StatusCode == http.StatusNotModified && cached != nil {
			_ = resp.Body.Close()
			return cached.Body, resp.StatusCode, resp.Header, true, nil
		}

		if shouldRetry(resp.StatusCode) && attempt < c.cfg.MaxRetries {
			_ = resp.Body.Close()
			if waitErr := c.cfg.SleepFn(ctx, backoffDuration(attempt)); waitErr != nil {
				return nil, 0, nil, false, waitErr
			}
			continue
		}

		buf, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, resp.Header, false, fmt.Errorf("politefetch: reading body: %w", readErr)
		}
		return buf, resp.StatusCode, resp.Header, false, nil
	}
	// Unreachable: the loop always either returns or continues.
	return nil, 0, nil, false, errors.New("politefetch: exhausted retry budget without sending a request")
}

// shouldRetry returns true for status codes that warrant a retry per the
// spec (§ Robots.txt and rate limiting): 429 and any 5xx.
func shouldRetry(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// sleepWithCtx is the default SleepFn — interruptible sleep.
func sleepWithCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
