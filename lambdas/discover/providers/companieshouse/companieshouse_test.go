package companieshouse

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

// --- fake HTTP doer --------------------------------------------------

type fakeHTTP struct {
	status int
	body   string
	gotReq *http.Request
	doErr  error
	calls  int
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	f.gotReq = req
	if f.doErr != nil {
		return nil, f.doErr
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
		BaseURL: "https://api.example.test",
		HTTP:    h,
	}
}

func validReq() discovery.FindRequest {
	return discovery.FindRequest{Vertical: "accountants", Location: "Manchester, UK"}
}

// --- happy path ------------------------------------------------------

func TestFind_HappyPath_DecodesAndConverts(t *testing.T) {
	body := `{"items":[
		{"title":"Acme Accountants Ltd","company_number":"01234567","company_status":"active"},
		{"title":"Beta Accountants","company_number":"02345678","company_status":"dissolved"}
	]}`
	h := &fakeHTTP{status: 200, body: body}
	p := provider(h)

	out, err := p.Find(context.Background(), validReq(), 50)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d items, want 2", len(out))
	}
	if out[0].Name != "Acme Accountants Ltd" {
		t.Errorf("name = %q", out[0].Name)
	}
	if out[0].Source != "companies-house" {
		t.Errorf("source = %q", out[0].Source)
	}
	if out[0].Domain != "" {
		t.Errorf("domain = %q (CH never carries websites)", out[0].Domain)
	}
	if out[0].SourceRefs["companiesHouseNumber"] != "01234567" {
		t.Errorf("companiesHouseNumber = %q", out[0].SourceRefs["companiesHouseNumber"])
	}
	// Active → high confidence; dissolved → lower.
	if out[0].Confidence <= out[1].Confidence {
		t.Errorf("expected active > dissolved confidence; got %v vs %v",
			out[0].Confidence, out[1].Confidence)
	}
}

func TestFind_BuildsExpectedQueryString(t *testing.T) {
	h := &fakeHTTP{status: 200, body: `{"items":[]}`}
	p := provider(h)

	_, err := p.Find(context.Background(), discovery.FindRequest{
		Vertical:        "accountants",
		Location:        "Manchester",
		IncludeKeywords: []string{"chartered", "tax"},
	}, 25)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	q := h.gotReq.URL.Query().Get("q")
	if !strings.Contains(q, "accountants") || !strings.Contains(q, "Manchester") || !strings.Contains(q, "chartered") {
		t.Errorf("query missing expected terms: %q", q)
	}
	if got := h.gotReq.URL.Query().Get("items_per_page"); got != "25" {
		t.Errorf("items_per_page = %q, want 25", got)
	}
}

func TestFind_AuthorizationHeaderUsesBasicWithAPIKey(t *testing.T) {
	h := &fakeHTTP{status: 200, body: `{"items":[]}`}
	p := provider(h)

	_, err := p.Find(context.Background(), validReq(), 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	auth := h.gotReq.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("Authorization missing Basic prefix: %q", auth)
	}
	// "test-key:" → dGVzdC1rZXk6
	if !strings.Contains(auth, "dGVzdC1rZXk6") {
		t.Errorf("Authorization doesn't carry APIKey: %q", auth)
	}
}

// --- error paths ----------------------------------------------------

func TestFind_MissingAPIKey(t *testing.T) {
	p := &Provider{HTTP: &fakeHTTP{}}
	_, err := p.Find(context.Background(), validReq(), 10)
	if err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Fatalf("expected APIKey error, got %v", err)
	}
}

func TestFind_EmptyQueryRejected(t *testing.T) {
	p := provider(&fakeHTTP{})
	_, err := p.Find(context.Background(), discovery.FindRequest{}, 10)
	if err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("expected empty-query error, got %v", err)
	}
}

func TestFind_401Returns_KeyRejected(t *testing.T) {
	p := provider(&fakeHTTP{status: 401, body: "unauthorized"})
	_, err := p.Find(context.Background(), validReq(), 10)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected API-key-rejected error, got %v", err)
	}
}

func TestFind_429Returns_RateLimitedHint(t *testing.T) {
	p := provider(&fakeHTTP{status: 429, body: ""})
	_, err := p.Find(context.Background(), validReq(), 10)
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("expected rate-limit error, got %v", err)
	}
}

func TestFind_5xxSurfacesBody(t *testing.T) {
	p := provider(&fakeHTTP{status: 503, body: "service unavailable"})
	_, err := p.Find(context.Background(), validReq(), 10)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected 503 error, got %v", err)
	}
}

func TestFind_HTTPDoErrorPropagates(t *testing.T) {
	p := provider(&fakeHTTP{doErr: errors.New("network down")})
	_, err := p.Find(context.Background(), validReq(), 10)
	if err == nil {
		t.Fatal("expected error")
	}
	var urlErr *url.Error // not what we expect, just confirming err is non-nil
	_ = errors.As(err, &urlErr)
	if !strings.Contains(err.Error(), "network down") {
		t.Errorf("err missing root cause: %v", err)
	}
}

// --- identity --------------------------------------------------------

func TestID_AndCost(t *testing.T) {
	p := &Provider{}
	if p.ID() != "companies-house" {
		t.Errorf("ID() = %q", p.ID())
	}
	if p.CostPerCallUSD() != 0 {
		t.Errorf("CostPerCallUSD() = %v, want 0", p.CostPerCallUSD())
	}
}
