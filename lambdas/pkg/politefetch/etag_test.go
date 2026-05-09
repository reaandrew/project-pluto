package politefetch

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestEtagPutSkippedWhenNoValidators(t *testing.T) {
	fake := setup(t)
	c := &etagCache{now: func() time.Time { return time.Now() }}

	if err := c.Put(context.Background(), "https://example.test/x", http.Header{}, []byte("body")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if fake.putCalls != 0 {
		t.Errorf("PutItem called %d times, want 0 (no ETag/Last-Modified)", fake.putCalls)
	}
}

func TestEtagPutAndGetRoundTrip(t *testing.T) {
	setup(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	c := &etagCache{now: func() time.Time { return now }}

	url := "https://example.test/page"
	hdr := http.Header{}
	hdr.Set("ETag", `"v1"`)
	hdr.Set("Last-Modified", "Fri, 08 May 2026 09:00:00 GMT")

	if err := c.Put(context.Background(), url, hdr, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := c.Get(context.Background(), url)
	if !ok {
		t.Fatal("Get returned false after Put")
	}
	if got.ETag != `"v1"` || got.LastModified != "Fri, 08 May 2026 09:00:00 GMT" || string(got.Body) != "hello" {
		t.Errorf("entry drift: %+v", got)
	}
}

func TestEtagGetReturnsFalseWhenExpired(t *testing.T) {
	setup(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	c := &etagCache{now: func() time.Time { return now }}

	url := "https://example.test/expired"
	hdr := http.Header{}
	hdr.Set("ETag", `"v1"`)
	if err := c.Put(context.Background(), url, hdr, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Advance clock past the 24h TTL.
	c.now = func() time.Time { return now.Add(48 * time.Hour) }
	if _, ok := c.Get(context.Background(), url); ok {
		t.Error("expected miss when entry expired")
	}
}

func TestEtagGetReturnsFalseWhenMissing(t *testing.T) {
	setup(t)
	c := &etagCache{now: func() time.Time { return time.Now() }}
	if _, ok := c.Get(context.Background(), "https://example.test/never-cached"); ok {
		t.Error("expected miss for never-cached URL")
	}
}

func TestEtagPutErrorWhenItemsTableUnset(t *testing.T) {
	setup(t)
	t.Setenv("ITEMS_TABLE", "")
	c := &etagCache{now: func() time.Time { return time.Now() }}

	hdr := http.Header{}
	hdr.Set("ETag", `"v1"`)
	if err := c.Put(context.Background(), "https://example.test/x", hdr, []byte("b")); err == nil {
		t.Error("expected error when ITEMS_TABLE unset")
	}
}
