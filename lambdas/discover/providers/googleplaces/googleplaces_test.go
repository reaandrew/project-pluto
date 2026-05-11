package googleplaces

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

// --- fake HTTP doer --------------------------------------------------

type fakeHTTP struct {
	status  int
	body    string
	gotReq  *http.Request
	gotBody []byte
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.gotReq = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.gotBody = b
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func provider(h *fakeHTTP) *Provider {
	return &Provider{
		APIKey:  "test-key",
		BaseURL: "https://places.example.test",
		HTTP:    h,
	}
}

func validReq() discovery.FindRequest {
	return discovery.FindRequest{Vertical: "accountants", Location: "Manchester, UK"}
}

// --- happy path ------------------------------------------------------

func TestFind_DecodesAndConverts(t *testing.T) {
	body := `{"places":[
		{
			"id":"places/abc",
			"displayName":{"text":"Acme Accountants","languageCode":"en"},
			"websiteUri":"https://www.acmeaccountants.co.uk/about",
			"formattedAddress":"1 High St, Manchester"
		},
		{
			"id":"places/def",
			"displayName":{"text":"Beta Bookkeeping"},
			"websiteUri":""
		}
	]}`
	h := &fakeHTTP{status: 200, body: body}
	p := provider(h)

	out, err := p.Find(context.Background(), validReq(), 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	// Domain normalised (scheme + www stripped + path dropped).
	if out[0].Domain != "acmeaccountants.co.uk" {
		t.Errorf("domain = %q, want acmeaccountants.co.uk", out[0].Domain)
	}
	if out[0].SourceRefs["googlePlaceId"] != "places/abc" {
		t.Errorf("placeId not surfaced: %+v", out[0].SourceRefs)
	}
	if out[0].SourceRefs["formattedAddress"] != "1 High St, Manchester" {
		t.Errorf("formattedAddress not surfaced: %+v", out[0].SourceRefs)
	}
	// No-website place gets a lower confidence.
	if out[0].Confidence <= out[1].Confidence {
		t.Errorf("with-website > no-website confidence expected: %v vs %v",
			out[0].Confidence, out[1].Confidence)
	}
}

func TestFind_RequestShape(t *testing.T) {
	h := &fakeHTTP{status: 200, body: `{"places":[]}`}
	p := provider(h)

	_, err := p.Find(context.Background(), discovery.FindRequest{
		Vertical:        "accountants",
		Location:        "Manchester",
		IncludeKeywords: []string{"chartered"},
	}, 15)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	// Method + URL.
	if h.gotReq.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", h.gotReq.Method)
	}
	if !strings.HasSuffix(h.gotReq.URL.Path, "/v1/places:searchText") {
		t.Errorf("path = %s", h.gotReq.URL.Path)
	}

	// Auth + FieldMask headers.
	if h.gotReq.Header.Get("X-Goog-Api-Key") != "test-key" {
		t.Errorf("API key header missing: %v", h.gotReq.Header)
	}
	if !strings.Contains(h.gotReq.Header.Get("X-Goog-FieldMask"), "places.websiteUri") {
		t.Errorf("FieldMask missing places.websiteUri: %s", h.gotReq.Header.Get("X-Goog-FieldMask"))
	}

	// Body: textQuery built from vertical + location + keywords; pageSize.
	var body map[string]any
	if err := json.Unmarshal(h.gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	q := body["textQuery"].(string)
	if !strings.Contains(q, "accountants") ||
		!strings.Contains(q, "Manchester") ||
		!strings.Contains(q, "chartered") {
		t.Errorf("textQuery missing expected terms: %q", q)
	}
	if int(body["pageSize"].(float64)) != 15 {
		t.Errorf("pageSize = %v, want 15", body["pageSize"])
	}
}

func TestFind_CapClampedAt20(t *testing.T) {
	h := &fakeHTTP{status: 200, body: `{"places":[]}`}
	_, err := provider(h).Find(context.Background(), validReq(), 9999)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(h.gotBody, &body)
	if int(body["pageSize"].(float64)) != 20 {
		t.Errorf("pageSize = %v, want 20 (clamped)", body["pageSize"])
	}
}

// --- error paths -----------------------------------------------------

func TestFind_MissingAPIKey(t *testing.T) {
	p := &Provider{HTTP: &fakeHTTP{}}
	if _, err := p.Find(context.Background(), validReq(), 10); err == nil ||
		!strings.Contains(err.Error(), "APIKey") {
		t.Errorf("expected APIKey error, got %v", err)
	}
}

func TestFind_403APIKeyRejected(t *testing.T) {
	p := provider(&fakeHTTP{status: 403, body: "PERMISSION_DENIED"})
	if _, err := p.Find(context.Background(), validReq(), 10); err == nil ||
		!strings.Contains(err.Error(), "rejected") {
		t.Errorf("expected key-rejected error, got %v", err)
	}
}

func TestFind_429RateLimited(t *testing.T) {
	p := provider(&fakeHTTP{status: 429, body: ""})
	if _, err := p.Find(context.Background(), validReq(), 10); err == nil ||
		!strings.Contains(err.Error(), "rate-limited") {
		t.Errorf("expected rate-limited error, got %v", err)
	}
}

func TestFind_5xxSurfacesBody(t *testing.T) {
	p := provider(&fakeHTTP{status: 500, body: "INTERNAL"})
	if _, err := p.Find(context.Background(), validReq(), 10); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}
}

// --- normaliseDomain ------------------------------------------------

func TestNormaliseDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.acmeaccountants.co.uk/about", "acmeaccountants.co.uk"},
		{"http://example.com", "example.com"},
		{"https://sub.example.com/path", "sub.example.com"},
		{"example.com", "example.com"},
		{"", ""},
		{"not-a-domain", ""}, // no dot → rejected
		{"https://", ""},     // empty host
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := normaliseDomain(c.in); got != c.want {
				t.Errorf("normaliseDomain(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- identity -------------------------------------------------------

func TestID_AndCost(t *testing.T) {
	p := &Provider{}
	if p.ID() != "google-places" {
		t.Errorf("ID() = %q", p.ID())
	}
	if p.CostPerCallUSD() != 0.017 {
		t.Errorf("CostPerCallUSD() = %v, want 0.017", p.CostPerCallUSD())
	}
}
