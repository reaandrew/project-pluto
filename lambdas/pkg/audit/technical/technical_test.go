package technical

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- heuristics ----------------------------------------------------------

func TestHeuristics_HTTPSFromURL(t *testing.T) {
	r := heuristics("https://example.com", []byte("<html></html>"))
	if !r.HTTPS {
		t.Error("HTTPS should be true for https URL")
	}
	r = heuristics("http://example.com", []byte("<html></html>"))
	if r.HTTPS {
		t.Error("HTTPS should be false for http URL")
	}
}

func TestHeuristics_ViewportMeta(t *testing.T) {
	with := `<html><head><meta name="viewport" content="width=device-width"/></head></html>`
	if !heuristics("https://x", []byte(with)).Viewport {
		t.Error("viewport with quoted attr should match")
	}
	withSingle := `<html><head><meta name='viewport' content='width=device-width'></head></html>`
	if !heuristics("https://x", []byte(withSingle)).Viewport {
		t.Error("viewport with single-quoted attr should match")
	}
	without := `<html><head><meta charset="utf-8"></head></html>`
	if heuristics("https://x", []byte(without)).Viewport {
		t.Error("viewport should not match plain meta tag")
	}
}

func TestHeuristics_FaviconLink(t *testing.T) {
	with := `<link rel="icon" href="/favicon.ico">`
	if !heuristics("https://x", []byte(with)).Favicon {
		t.Error("favicon link should match")
	}
	withShortcut := `<link rel="shortcut icon" href="/fav.ico">`
	if !heuristics("https://x", []byte(withShortcut)).Favicon {
		t.Error("shortcut icon should match")
	}
	withApple := `<link rel="apple-touch-icon" href="/apple-touch.png">`
	if !heuristics("https://x", []byte(withApple)).Favicon {
		t.Error("apple-touch-icon should match (contains 'icon')")
	}
	without := `<link rel="stylesheet" href="/x.css">`
	if heuristics("https://x", []byte(without)).Favicon {
		t.Error("stylesheet link should not match favicon")
	}
}

func TestHeuristics_ContactDetected(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"email", `<p>contact: hello@example.com</p>`, true},
		{"mailto href", `<a href="mailto:owner@x.co.uk">email</a>`, true},
		{"uk phone with spaces", `<p>Tel: 0161 234 5678</p>`, true},
		{"uk phone +44", `<p>Call +44 161 234 5678</p>`, true},
		{"no contact", `<p>just words</p>`, false},
		{"bare number string", `<p>page hit 12345 times</p>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := heuristics("https://x", []byte(c.body)).ContactDetected
			if got != c.want {
				t.Errorf("body %q → ContactDetected=%v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestHeuristics_HomepageHashIsDeterministic(t *testing.T) {
	r1 := heuristics("https://x", []byte("hello world"))
	r2 := heuristics("https://x", []byte("hello world"))
	if r1.HomepageHash == "" {
		t.Fatal("HomepageHash is empty")
	}
	if r1.HomepageHash != r2.HomepageHash {
		t.Errorf("hash not deterministic: %q vs %q", r1.HomepageHash, r2.HomepageHash)
	}
	r3 := heuristics("https://x", []byte("hello world!"))
	if r3.HomepageHash == r1.HomepageHash {
		t.Errorf("hash should differ for different body")
	}
}

// --- WorthQualitativeAudit ---------------------------------------------

func TestWorthQualitativeAudit_ClearlyWeakSiteAudits(t *testing.T) {
	r := Result{} // no HTTPS, no viewport, no favicon, no contact, no scores
	if !WorthQualitativeAudit(r, 30) {
		t.Errorf("a totally-zero result should exceed threshold 30")
	}
}

func TestWorthQualitativeAudit_StrongSiteSkips(t *testing.T) {
	r := Result{
		HTTPS: true, Viewport: true, Favicon: true, ContactDetected: true,
		Lighthouse: Lighthouse{Performance: 90, Accessibility: 90, SEO: 90},
	}
	if WorthQualitativeAudit(r, 30) {
		t.Errorf("a strong site should be below threshold")
	}
}

func TestWorthQualitativeAudit_BorderlineLighthouseDrops(t *testing.T) {
	r := Result{
		HTTPS: true, Viewport: true, Favicon: true, ContactDetected: true,
		Lighthouse: Lighthouse{Performance: 30, Accessibility: 65, SEO: 65},
	}
	// 60-30 = 30 issue points; exactly the threshold.
	if !WorthQualitativeAudit(r, 30) {
		t.Errorf("performance 30 + threshold 30 should match")
	}
}

func TestWorthQualitativeAudit_ZeroLighthouseIsIgnored(t *testing.T) {
	r := Result{
		HTTPS: true, Viewport: true, Favicon: true, ContactDetected: true,
		Lighthouse: Lighthouse{Performance: 0, Accessibility: 0, SEO: 0},
	}
	// Zero scores → PSI didn't run; we don't penalise that.
	if WorthQualitativeAudit(r, 30) {
		t.Errorf("zero lighthouse (PSI down) shouldn't trigger audit when heuristics are fine")
	}
}

// --- Auditor (homepage fetch + PSI) ----------------------------------

type fakeFetcher struct {
	resp *PoliteResponse
	err  error
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string) (*PoliteResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

type fakeHTTP struct {
	status int
	body   string
	doErr  error
	gotReq *http.Request
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
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

const homepageHTML = `<!doctype html><html><head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" href="/favicon.ico">
</head><body><a href="mailto:hi@acme.co.uk">contact</a></body></html>`

func TestAudit_HomepageOnly_NoPSI(t *testing.T) {
	a := &Auditor{
		Polite: &fakeFetcher{resp: &PoliteResponse{StatusCode: 200, Body: []byte(homepageHTML)}},
	}
	r, err := a.Audit(context.Background(), "https://acme.co.uk")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if !r.HTTPS || !r.Viewport || !r.Favicon || !r.ContactDetected {
		t.Errorf("heuristics underpopulated: %+v", r)
	}
	if r.Lighthouse.Performance != 0 {
		t.Errorf("Lighthouse should be zero when PSI is skipped: %+v", r.Lighthouse)
	}
	if r.HomepageHash == "" {
		t.Error("HomepageHash should be set")
	}
}

func TestAudit_WithPageSpeed(t *testing.T) {
	psi := `{"lighthouseResult":{"categories":{
		"performance":{"score":0.42},
		"accessibility":{"score":0.71},
		"seo":{"score":0.68}
	}}}`
	http := &fakeHTTP{status: 200, body: psi}
	a := &Auditor{
		PageSpeedAPIKey: "test-key",
		HTTP:            http,
		Polite:          &fakeFetcher{resp: &PoliteResponse{StatusCode: 200, Body: []byte(homepageHTML)}},
	}
	r, err := a.Audit(context.Background(), "https://acme.co.uk")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if r.Lighthouse.Performance != 42 || r.Lighthouse.Accessibility != 71 || r.Lighthouse.SEO != 68 {
		t.Errorf("Lighthouse scores wrong: %+v", r.Lighthouse)
	}
	// Confirm API key is on the request.
	if !strings.Contains(http.gotReq.URL.RawQuery, "key=test-key") {
		t.Errorf("API key missing from query: %s", http.gotReq.URL.RawQuery)
	}
}

func TestAudit_PageSpeedFailure_DegradesGracefully(t *testing.T) {
	a := &Auditor{
		PageSpeedAPIKey: "test-key",
		HTTP:            &fakeHTTP{doErr: errors.New("psi down")},
		Polite:          &fakeFetcher{resp: &PoliteResponse{StatusCode: 200, Body: []byte(homepageHTML)}},
	}
	r, err := a.Audit(context.Background(), "https://acme.co.uk")
	if err != nil {
		t.Fatalf("Audit should not fail when only PSI fails: %v", err)
	}
	// Heuristics still populated.
	if !r.HTTPS {
		t.Error("heuristics should still be populated")
	}
	// Lighthouse stays zeroed.
	if r.Lighthouse.Performance != 0 {
		t.Errorf("Lighthouse should be zero on PSI fail: %+v", r.Lighthouse)
	}
}

func TestAudit_HomepageFetchFailure_Returns(t *testing.T) {
	a := &Auditor{
		Polite: &fakeFetcher{err: errors.New("dns down")},
	}
	_, err := a.Audit(context.Background(), "https://acme.co.uk")
	if err == nil {
		t.Fatal("expected error when homepage fetch fails")
	}
}

func TestAudit_Homepage4xx_Returns(t *testing.T) {
	a := &Auditor{
		Polite: &fakeFetcher{resp: &PoliteResponse{StatusCode: 404, Body: []byte("not found")}},
	}
	_, err := a.Audit(context.Background(), "https://acme.co.uk")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestAudit_EmptyURL_Rejected(t *testing.T) {
	a := &Auditor{Polite: &fakeFetcher{}}
	_, err := a.Audit(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

// --- pct ----------------------------------------------------------------

func TestPct_RoundsAndClamps(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0.42, 42},
		{0.999, 100},
		{1.0, 100},
		{0, 0},
		{-0.1, 0},  // clamped
		{1.5, 100}, // clamped
	}
	for _, c := range cases {
		if got := pct(c.in); got != c.want {
			t.Errorf("pct(%v)=%d, want %d", c.in, got, c.want)
		}
	}
}
