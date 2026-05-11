package politefetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- shared fakes ---------------------------------------------------------

type fakeDDB struct {
	store            map[string]map[string]dtypes.AttributeValue
	getCalls         int
	putCalls         int
	updateCalls      int
	updateFailNTimes int
}

func newFakeDDB() *fakeDDB {
	return &fakeDDB{store: make(map[string]map[string]dtypes.AttributeValue)}
}

func keyOf(in map[string]dtypes.AttributeValue) string {
	pk := in["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in["sk"].(*dtypes.AttributeValueMemberS).Value
	return pk + "|" + sk
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putCalls++
	f.store[keyOf(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.getCalls++
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Key["sk"].(*dtypes.AttributeValueMemberS).Value
	if item, ok := f.store[pk+"|"+sk]; ok {
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updateCalls++

	if f.updateFailNTimes > 0 {
		f.updateFailNTimes--
		// Surface a conditional-check failure carrying the existing item so
		// estimateWait can read lastFetchAt from it.
		return nil, &dtypes.ConditionalCheckFailedException{
			Item: f.store[keyOf(in.Key)],
		}
	}

	// Apply the update naively: just overwrite lastFetchAt to :now.
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Key["sk"].(*dtypes.AttributeValueMemberS).Value
	now := in.ExpressionAttributeValues[":now"]
	if existing, ok := f.store[pk+"|"+sk]; ok {
		existing["lastFetchAt"] = now
		f.store[pk+"|"+sk] = existing
	} else {
		f.store[pk+"|"+sk] = map[string]dtypes.AttributeValue{
			"pk":          in.Key["pk"],
			"sk":          in.Key["sk"],
			"lastFetchAt": now,
		}
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}

func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

// setup primes ITEMS_TABLE + a fresh fakeDDB. Returns the fake.
func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	fake := newFakeDDB()
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return fake
}

// --- end-to-end Fetch ----------------------------------------------------

func TestFetchHappyPath(t *testing.T) {
	setup(t)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprintln(w, "User-agent: *")
			fmt.Fprintln(w, "Allow: /")
		case "/page":
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprintln(w, "hello")
		}
	}))
	defer server.Close()

	c := newClientForTest(server)
	resp, err := c.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "hello") {
		t.Errorf("body = %q", resp.Body)
	}
	if got := resp.Header.Get("User-Agent"); got != "" {
		t.Errorf("response header should not echo UA in test (sanity): %q", got)
	}
}

// TestFetchHonoursRobotsDisallow verifies a Disallow rule blocks the fetch.
func TestFetchHonoursRobotsDisallow(t *testing.T) {
	setup(t)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprintln(w, "User-agent: *")
			fmt.Fprintln(w, "Disallow: /private/")
		default:
			t.Errorf("server should not be hit beyond robots, got %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := newClientForTest(server)
	_, err := c.Fetch(context.Background(), server.URL+"/private/secret")
	if !errors.Is(err, ErrDisallowed) {
		t.Errorf("expected ErrDisallowed, got %v", err)
	}
}

// TestFetch429RetriesThenSucceeds verifies the retry/backoff loop.
func TestFetch429RetriesThenSucceeds(t *testing.T) {
	setup(t)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(404)
		case "/page":
			n := atomic.AddInt32(&hits, 1)
			if n < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprintln(w, "ok-after-retry")
		}
	}))
	defer server.Close()

	c := newClientForTest(server)
	resp, err := c.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("expected 3 attempts, got %d", hits)
	}
}

// TestFetch5xxExhaustsRetriesAndReturnsLastResponse keeps failing 500s and
// confirms the final response surface.
func TestFetch5xxExhaustsRetriesAndReturnsLastResponse(t *testing.T) {
	setup(t)
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(404)
		case "/page":
			atomic.AddInt32(&hits, 1)
			http.Error(w, "boom", 503)
		}
	}))
	defer server.Close()

	c := newClientForTest(server)
	resp, err := c.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("expected final 503, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 4 { // 1 initial + 3 retries
		t.Errorf("expected 4 attempts, got %d", got)
	}
}

// TestFetchETagShortCircuitsOn304 verifies the ETag cache.
func TestFetchETagShortCircuitsOn304(t *testing.T) {
	setup(t)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(404)
		case "/page":
			atomic.AddInt32(&hits, 1)
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprintln(w, "fresh-body")
		}
	}))
	defer server.Close()

	c := newClientForTest(server)
	first, err := c.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch first: %v", err)
	}
	if first.FromCache {
		t.Errorf("first fetch should not be from cache")
	}

	second, err := c.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch second: %v", err)
	}
	if !second.FromCache {
		t.Errorf("second fetch should be FromCache=true")
	}
	if !strings.Contains(string(second.Body), "fresh-body") {
		t.Errorf("cached body wrong: %q", second.Body)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("expected 2 page hits (one full, one 304), got %d", hits)
	}
}

func TestFetchRejectsNonHTTPSchemes(t *testing.T) {
	setup(t)
	c := New(testConfig())
	if _, err := c.Fetch(context.Background(), "ftp://example.com/foo"); err == nil {
		t.Error("expected scheme rejection")
	}
}

func TestNewFillsDefaults(t *testing.T) {
	c := New(Config{})
	if c.cfg.UserAgent != DefaultUserAgent {
		t.Errorf("default UserAgent = %q", c.cfg.UserAgent)
	}
	if c.cfg.Throttle != DefaultThrottle {
		t.Errorf("default Throttle = %v", c.cfg.Throttle)
	}
	if c.cfg.MaxRetries != DefaultMaxRetries {
		t.Errorf("default MaxRetries = %d", c.cfg.MaxRetries)
	}
	if c.cfg.HTTP == nil {
		t.Error("default HTTP client is nil")
	}
	if c.cfg.Now == nil || c.cfg.SleepFn == nil {
		t.Error("default Now/SleepFn not wired")
	}
}

func TestSleepWithCtxReturnsImmediatelyOnZero(t *testing.T) {
	if err := sleepWithCtx(context.Background(), 0); err != nil {
		t.Errorf("zero-duration sleep returned %v", err)
	}
}

func TestSleepWithCtxRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := sleepWithCtx(ctx, time.Hour); err == nil {
		t.Error("expected context-cancellation error")
	}
}

func TestSleepWithCtxSleepsBriefly(t *testing.T) {
	start := time.Now()
	if err := sleepWithCtx(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatalf("sleepWithCtx: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		// hold the request open until the context cancels
		<-r.Context().Done()
	}))
	defer server.Close()

	c := newClientForTest(server)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Fetch(ctx, server.URL+"/slow")
	if err == nil {
		t.Error("expected a context-cancellation error")
	}
}

// --- helpers --------------------------------------------------------------

func testConfig() Config {
	return Config{
		UserAgent:  "TestAgent/1.0",
		Throttle:   1 * time.Millisecond,
		MaxRetries: 3,
		HTTP:       &http.Client{Timeout: 5 * time.Second},
		Now:        func() time.Time { return time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC) },
		// Tests don't sleep — backoff and throttle are deterministic.
		SleepFn: func(_ context.Context, _ time.Duration) error { return nil },
	}
}

func newClientForTest(server *httptest.Server) *Client {
	cfg := testConfig()
	cfg.HTTP = server.Client()
	c := New(cfg)
	// rewire the robots cache's HTTP client onto the test server too — robots
	// is fetched from https://<host>/robots.txt by default; for httptest we
	// want it to go through the test server's transport.
	c.robots.http = server.Client()
	return c
}
