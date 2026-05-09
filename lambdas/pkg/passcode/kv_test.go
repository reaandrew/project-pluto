package passcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// withFakeCloudflare points kvBaseURL at a httptest server for the test's
// duration and returns the server. The handler argument inspects the
// request and responds however the test wants.
func withFakeCloudflare(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := kvBaseURL
	kvBaseURL = srv.URL
	t.Cleanup(func() {
		kvBaseURL = prev
		srv.Close()
	})
	return srv
}

func successResponse() string {
	return `{"success":true,"errors":[],"messages":[],"result":null}`
}

// --- Put -----------------------------------------------------------------

func TestKVPutSendsExpectedRequest(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotCT     string
		gotBody   string
	)
	srv := withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(successResponse()))
	})
	_ = srv

	w := NewKVWriter("acct-1", "ns-2", "tok-3")
	if err := w.Put(context.Background(), "site-abc", "hash-deadbeef", map[string]string{"issuedAt": "2026-05-09"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s", gotMethod)
	}
	if !strings.Contains(gotPath, "/accounts/acct-1/storage/kv/namespaces/ns-2/bulk") {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-3" {
		t.Errorf("auth = %s", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %s", gotCT)
	}
	// Body should be a JSON array with our entry.
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
		t.Fatalf("body not valid JSON: %v\n%s", err, gotBody)
	}
	if len(parsed) != 1 || parsed[0]["key"] != "site-abc" || parsed[0]["value"] != "hash-deadbeef" {
		t.Errorf("body shape unexpected: %s", gotBody)
	}
	meta, ok := parsed[0]["metadata"].(map[string]any)
	if !ok || meta["issuedAt"] != "2026-05-09" {
		t.Errorf("metadata not threaded: %v", parsed[0]["metadata"])
	}
}

func TestKVPutErrorOnCloudflare4xx(t *testing.T) {
	withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	})
	w := NewKVWriter("acct", "ns", "tok")
	err := w.Put(context.Background(), "key", "val", nil)
	if err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("expected Cloudflare auth error, got %v", err)
	}
}

func TestKVPutErrorOnSuccessFalseStatusOK(t *testing.T) {
	withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		// Cloudflare can return 200 with success=false.
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":42,"message":"namespace not found"}]}`))
	})
	w := NewKVWriter("acct", "ns", "tok")
	err := w.Put(context.Background(), "key", "val", nil)
	if err == nil || !strings.Contains(err.Error(), "namespace not found") {
		t.Errorf("expected success=false error, got %v", err)
	}
}

func TestKVPutValidatesArgs(t *testing.T) {
	cases := map[string]*KVWriter{
		"empty account":   NewKVWriter("", "ns", "tok"),
		"empty namespace": NewKVWriter("acct", "", "tok"),
		"empty token":     NewKVWriter("acct", "ns", ""),
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			if err := w.Put(context.Background(), "k", "v", nil); err == nil {
				t.Errorf("expected validate error for %q", name)
			}
		})
	}

	good := NewKVWriter("acct", "ns", "tok")
	withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(successResponse()))
	})
	if err := good.Put(context.Background(), "", "v", nil); err == nil {
		t.Error("expected error for empty key")
	}
	if err := good.Put(context.Background(), "k", "", nil); err == nil {
		t.Error("expected error for empty value")
	}
}

// --- Delete --------------------------------------------------------------

func TestKVDeleteSendsExpectedRequest(t *testing.T) {
	var calls int32
	withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/values/site-abc") {
			t.Errorf("path = %s, expected /values/site-abc suffix", r.URL.Path)
		}
		_, _ = w.Write([]byte(successResponse()))
	})
	w := NewKVWriter("acct", "ns", "tok")
	if err := w.Delete(context.Background(), "site-abc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 DELETE call, got %d", calls)
	}
}

func TestKVDeleteValidatesArgs(t *testing.T) {
	w := NewKVWriter("acct", "ns", "tok")
	if err := w.Delete(context.Background(), ""); err == nil {
		t.Error("expected error for empty key")
	}
	bad := NewKVWriter("", "", "")
	if err := bad.Delete(context.Background(), "k"); err == nil {
		t.Error("expected error for empty writer config")
	}
}

func TestKVDeleteErrorOn5xx(t *testing.T) {
	withFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 503)
	})
	w := NewKVWriter("acct", "ns", "tok")
	if err := w.Delete(context.Background(), "key"); err == nil {
		t.Error("expected 5xx error")
	}
}

// --- SetHTTPClient -------------------------------------------------------

func TestSetHTTPClientReplacesClient(t *testing.T) {
	w := NewKVWriter("acct", "ns", "tok")
	custom := &http.Client{}
	w.SetHTTPClient(custom)
	if w.http != custom {
		t.Error("SetHTTPClient did not swap")
	}
}
