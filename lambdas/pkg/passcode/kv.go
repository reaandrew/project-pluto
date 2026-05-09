package passcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// kvBaseURL is the Cloudflare REST API base. Overridable for tests.
var kvBaseURL = "https://api.cloudflare.com/client/v4"

// DefaultKVTimeout caps any single Cloudflare API call. The Lambda's
// overall request budget is finite (15 min hard cap, 30s typical for
// publisher); 10s is plenty for a KV write.
const DefaultKVTimeout = 10 * time.Second

// KVWriter writes passcode hashes to a Cloudflare Workers KV namespace
// via the Cloudflare REST API. The Worker reads the same key+namespace
// to validate submitted passcodes; revocation is "delete the key".
type KVWriter struct {
	accountID   string
	namespaceID string
	apiToken    string
	http        *http.Client
}

// NewKVWriter builds a writer pointed at the per-env namespace. accountID
// is the Cloudflare account UUID; namespaceID is the KV-namespace UUID
// from `terraform output -raw kv_namespace_id`; apiToken is a Cloudflare
// API token with `Workers KV Storage:Edit` scope.
func NewKVWriter(accountID, namespaceID, apiToken string) *KVWriter {
	return &KVWriter{
		accountID:   accountID,
		namespaceID: namespaceID,
		apiToken:    apiToken,
		http:        &http.Client{Timeout: DefaultKVTimeout},
	}
}

// SetHTTPClient lets tests inject an httptest server's client. Production
// callers leave it alone.
func (w *KVWriter) SetHTTPClient(c *http.Client) { w.http = c }

// kvResponse is the standard Cloudflare REST envelope.
type kvResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// Put writes value under key in the KV namespace, with optional metadata
// (e.g. {"issuedAt": "..."}). Existing values are overwritten. Returns
// nil on success or an error on a non-2xx response (including the
// Cloudflare error message when present).
func (w *KVWriter) Put(ctx context.Context, key, value string, metadata map[string]string) error {
	if err := w.validate(); err != nil {
		return err
	}
	if key == "" || value == "" {
		return errors.New("passcode: KV Put requires key and value")
	}

	// Cloudflare KV's "PUT key/value" endpoint accepts a multipart-encoded
	// body to attach metadata. We use the simpler "PUT bulk" form with a
	// single entry — fewer encoding bugs.
	type bulkEntry struct {
		Key      string            `json:"key"`
		Value    string            `json:"value"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	body, err := json.Marshal([]bulkEntry{{Key: key, Value: value, Metadata: metadata}})
	if err != nil {
		return fmt.Errorf("passcode: marshalling KV bulk entry: %w", err)
	}

	endpoint := fmt.Sprintf("%s/accounts/%s/storage/kv/namespaces/%s/bulk",
		kvBaseURL, url.PathEscape(w.accountID), url.PathEscape(w.namespaceID))
	return w.do(ctx, http.MethodPut, endpoint, "application/json", strings.NewReader(string(body)))
}

// Delete removes the entry at key (revocation path). Returns nil on
// success, even when the key doesn't exist (Cloudflare returns 200 in
// either case).
func (w *KVWriter) Delete(ctx context.Context, key string) error {
	if err := w.validate(); err != nil {
		return err
	}
	if key == "" {
		return errors.New("passcode: KV Delete requires key")
	}
	endpoint := fmt.Sprintf("%s/accounts/%s/storage/kv/namespaces/%s/values/%s",
		kvBaseURL,
		url.PathEscape(w.accountID),
		url.PathEscape(w.namespaceID),
		url.PathEscape(key),
	)
	return w.do(ctx, http.MethodDelete, endpoint, "", nil)
}

func (w *KVWriter) validate() error {
	switch {
	case w.accountID == "":
		return errors.New("passcode: KV writer accountID is empty")
	case w.namespaceID == "":
		return errors.New("passcode: KV writer namespaceID is empty")
	case w.apiToken == "":
		return errors.New("passcode: KV writer apiToken is empty")
	}
	return nil
}

func (w *KVWriter) do(ctx context.Context, method, endpoint, contentType string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("passcode: building KV request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.apiToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("passcode: Cloudflare KV %s: %w", method, err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("passcode: Cloudflare KV %s returned %d: %s", method, resp.StatusCode, string(bodyBytes))
	}
	// Some endpoints (Delete) return success without a body; only parse
	// JSON when there's something to parse.
	if len(bodyBytes) == 0 {
		return nil
	}
	var parsed kvResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		// Non-JSON 2xx is unexpected but treat as success — we only fail
		// on explicit Cloudflare errors.
		return nil
	}
	if !parsed.Success {
		if len(parsed.Errors) > 0 {
			return fmt.Errorf("passcode: Cloudflare KV %s: %d %s",
				method, parsed.Errors[0].Code, parsed.Errors[0].Message)
		}
		return fmt.Errorf("passcode: Cloudflare KV %s: success=false with no error detail", method)
	}
	return nil
}
