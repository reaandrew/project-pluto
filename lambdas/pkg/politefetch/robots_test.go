package politefetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestRobotsCachesAcrossCalls(t *testing.T) {
	setup(t)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			atomic.AddInt32(&hits, 1)
			fmt.Fprintln(w, "User-agent: *")
			fmt.Fprintln(w, "Allow: /")
			return
		}
	}))
	defer server.Close()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      server.Client(),
		now:       func() time.Time { return now },
	}
	u, _ := url.Parse(server.URL + "/page")
	for i := 0; i < 3; i++ {
		allowed, _, err := rc.Allowed(context.Background(), u)
		if err != nil {
			t.Fatalf("Allowed: %v", err)
		}
		if !allowed {
			t.Errorf("call %d: not allowed", i)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("robots.txt fetched %d times across 3 calls, want 1 (in-process memo)", got)
	}
}

func TestRobotsTreatsServer5xxAsDisallowAll(t *testing.T) {
	setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Error(w, "boom", 503)
			return
		}
	}))
	defer server.Close()

	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      server.Client(),
		now:       func() time.Time { return time.Now() },
	}
	u, _ := url.Parse(server.URL + "/page")
	allowed, _, err := rc.Allowed(context.Background(), u)
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if allowed {
		t.Error("should be disallowed when robots.txt returns 5xx (RFC 9309)")
	}
}

func TestRobotsTreats404AsAllowAll(t *testing.T) {
	setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer server.Close()

	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      server.Client(),
		now:       func() time.Time { return time.Now() },
	}
	u, _ := url.Parse(server.URL + "/anywhere")
	allowed, _, err := rc.Allowed(context.Background(), u)
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if !allowed {
		t.Error("should be allowed when robots.txt is 404")
	}
}

func TestRobotsLoadsFromDDBCacheWithoutNetwork(t *testing.T) {
	setup(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	// Pre-seed the DDB items table with a robots cache row that's still fresh.
	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      &http.Client{Transport: &failTransport{}}, // any HTTP call fails
		now:       func() time.Time { return now },
	}
	if err := rc.writeDDB(context.Background(), "example.test:80", []byte("User-agent: *\nDisallow: /\n"), now.Add(robotsTTL)); err != nil {
		t.Fatalf("writeDDB: %v", err)
	}

	u, _ := url.Parse("http://example.test:80/secret")
	allowed, _, err := rc.Allowed(context.Background(), u)
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if allowed {
		t.Error("expected disallowed (cache had Disallow: /)")
	}
}

// failTransport panics if used — proves no network call was made.
type failTransport struct{}

func (failTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network call must not happen")
}

func TestRobotsAllowedPropagatesLookupError(t *testing.T) {
	setup(t)
	t.Setenv("ITEMS_TABLE", "") // forces readDDB to fail

	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      &http.Client{Transport: &failTransport{}},
		now:       func() time.Time { return time.Now() },
	}
	u, _ := url.Parse("https://example.test/x")
	if _, _, err := rc.Allowed(context.Background(), u); err == nil {
		t.Error("expected error when ITEMS_TABLE unset")
	}
}

func TestRobotsExposesCrawlDelay(t *testing.T) {
	setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprintln(w, "User-agent: *")
			fmt.Fprintln(w, "Crawl-delay: 7")
			fmt.Fprintln(w, "Allow: /")
		}
	}))
	defer server.Close()

	rc := &robotsCache{
		userAgent: "TestAgent/1.0",
		http:      server.Client(),
		now:       func() time.Time { return time.Now() },
	}
	u, _ := url.Parse(server.URL + "/page")
	_, delay, err := rc.Allowed(context.Background(), u)
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if delay != 7*time.Second {
		t.Errorf("Crawl-delay = %v, want 7s", delay)
	}
}
