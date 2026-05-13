// Package technical runs the cheap pre-audit pass that gates the
// (expensive) Bedrock qualitative audit downstream. Two sources:
//
//   - PageSpeed Insights API (free up to 25k/day; Google API key in
//     SSM). Returns three Lighthouse category scores: performance,
//     accessibility, SEO.
//   - HTML heuristics on the homepage. Cheap signal extraction: HTTPS,
//     viewport meta, favicon link, and a contact-info match (email or
//     UK phone pattern).
//
// The combined Result mirrors the `technical` block of the Audit row
// in .ralph/specs/02-data-model.md. The audit Lambda (iter 2.3) gates
// the qualitative call on a threshold against this Result — see
// `WorthQualitativeAudit` for the decision rule.
//
// politefetch IS used here: the homepage fetch is the canonical
// scrape-style target (robots.txt applies, per-host throttle applies).
package technical

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Result is the technical-pass output. The audit Lambda persists this
// as the `technical` field on the Audit row; downstream consumers
// (qualifier, frontend) read individual fields.
type Result struct {
	HTTPS           bool       `json:"https"`
	Viewport        bool       `json:"viewport"`
	Favicon         bool       `json:"favicon"`
	ContactDetected bool       `json:"contactDetected"`
	Lighthouse      Lighthouse `json:"lighthouse"`
	// HomepageHash is the SHA-256 of the fetched HTML. Used by the
	// qualitative-audit cache key (domain, html_hash) — same content
	// → cache hit → skip Bedrock spend.
	HomepageHash string `json:"homepageHash,omitempty"`
}

type Lighthouse struct {
	Performance   int `json:"performance"`
	Accessibility int `json:"accessibility"`
	SEO           int `json:"seo"`
}

// HTTPDoer is the subset of *http.Client used for PageSpeed calls.
// Tests inject a fake.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// PoliteFetcher is the subset of *politefetch.Client used for the
// homepage scrape. Defined as an interface so tests don't have to
// build a full politefetch.Client. Implementations honour robots.txt
// + per-host throttle.
type PoliteFetcher interface {
	Fetch(ctx context.Context, urlStr string) (*PoliteResponse, error)
}

// PoliteResponse mirrors politefetch.Response — kept here so the
// interface stays decoupled from the politefetch package's struct.
type PoliteResponse struct {
	StatusCode int
	Body       []byte
}

// Auditor runs the technical pass against a single URL.
type Auditor struct {
	PageSpeedAPIKey string
	HTTP            HTTPDoer
	Polite          PoliteFetcher
}

// Audit fetches the homepage, computes heuristics, and runs the
// PageSpeed scan. PageSpeed failures degrade gracefully — the
// Lighthouse fields are zeroed and the function still returns a
// usable Result with heuristics populated. That keeps the
// downstream gate (WorthQualitativeAudit) functional even when
// Google's API has a bad day.
//
// Returns an error only when the homepage fetch fails — without
// HTML there's nothing the qualitative audit can chew on, so the
// caller should DLQ that case.
func (a *Auditor) Audit(ctx context.Context, pageURL string) (Result, error) {
	if pageURL == "" {
		return Result{}, errors.New("technical: page URL is required")
	}
	pf := a.Polite
	if pf == nil {
		return Result{}, errors.New("technical: PoliteFetcher is required")
	}
	resp, err := pf.Fetch(ctx, pageURL)
	if err != nil {
		return Result{}, fmt.Errorf("technical: fetching homepage %s: %w", pageURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("technical: homepage returned HTTP %d", resp.StatusCode)
	}

	r := heuristics(pageURL, resp.Body)

	// PageSpeed — best-effort. If the key is empty (local dev) or
	// the API call fails, leave the Lighthouse fields zeroed and
	// keep going.
	if a.PageSpeedAPIKey != "" && a.HTTP != nil {
		ls, err := a.runPageSpeed(ctx, pageURL)
		if err == nil {
			r.Lighthouse = ls
		}
		// On error, drop it on the floor. The qualitative-audit
		// gate falls back to the heuristics-only signal, which is
		// still useful.
	}
	return r, nil
}

// heuristics inspects the page URL + HTML body and fills the cheap
// fields. Exposed so tests can call it directly without a fetcher.
func heuristics(pageURL string, body []byte) Result {
	r := Result{}
	if u, err := url.Parse(pageURL); err == nil && strings.EqualFold(u.Scheme, "https") {
		r.HTTPS = true
	}
	// Hash the body once — cache key for the qualitative pass.
	sum := sha256.Sum256(body)
	r.HomepageHash = hex.EncodeToString(sum[:])

	// Lower-cased copy for case-insensitive matching of tag/attr names.
	// We keep the original body for content-pattern matches that should
	// stay case-sensitive (none today, but cheap to keep both).
	lc := strings.ToLower(string(body))

	r.Viewport = viewportRE.MatchString(lc)
	r.Favicon = faviconRE.MatchString(lc)
	r.ContactDetected = emailRE.MatchString(lc) || phoneUKRE.MatchString(lc) || mailtoRE.MatchString(lc)
	return r
}

// runPageSpeed calls the PageSpeed Insights API and converts the
// three category scores we care about to 0..100 ints. PSI returns
// scores as 0..1 floats; we multiply and round.
func (a *Auditor) runPageSpeed(ctx context.Context, pageURL string) (Lighthouse, error) {
	u, err := url.Parse("https://www.googleapis.com/pagespeedonline/v5/runPagespeed")
	if err != nil {
		return Lighthouse{}, err
	}
	q := u.Query()
	q.Set("url", pageURL)
	q.Set("key", a.PageSpeedAPIKey)
	q.Add("category", "performance")
	q.Add("category", "accessibility")
	q.Add("category", "seo")
	q.Set("strategy", "mobile")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Lighthouse{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return Lighthouse{}, fmt.Errorf("pagespeed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Lighthouse{}, fmt.Errorf("pagespeed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var page pagespeedResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return Lighthouse{}, fmt.Errorf("pagespeed: decode: %w", err)
	}
	cats := page.LighthouseResult.Categories
	return Lighthouse{
		Performance:   pct(cats.Performance.Score),
		Accessibility: pct(cats.Accessibility.Score),
		SEO:           pct(cats.SEO.Score),
	}, nil
}

// WorthQualitativeAudit decides whether the (expensive) Bedrock
// qualitative pass is worth running. Threshold values match the
// spec's `minTechnicalIssueScore` (PipelineSettings.Thresholds,
// default 30): if EITHER the heuristics or the Lighthouse averages
// fall below the threshold, we treat the site as "weak enough to
// audit further." A site that passes all heuristics AND has solid
// Lighthouse scores isn't a redesign candidate.
//
// Pure function. Exposed for the audit Lambda (iter 2.3) + tests.
func WorthQualitativeAudit(r Result, minTechnicalIssueScore int) bool {
	issues := 0
	if !r.HTTPS {
		issues += 30
	}
	if !r.Viewport {
		issues += 25
	}
	if !r.Favicon {
		issues += 5
	}
	if !r.ContactDetected {
		issues += 10
	}
	// Lighthouse — anything < 60 in any category contributes.
	for _, s := range []int{r.Lighthouse.Performance, r.Lighthouse.Accessibility, r.Lighthouse.SEO} {
		if s == 0 {
			continue // PageSpeed didn't run or returned 0 (rare); ignore
		}
		if s < 60 {
			issues += (60 - s) // 0..60 maps to 60..0 points
		}
	}
	return issues >= minTechnicalIssueScore
}

func pct(score float64) int {
	v := score * 100
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return int(v + 0.5)
}

// Heuristic regexes. Compiled once; reused across calls.
var (
	viewportRE = regexp.MustCompile(`<meta[^>]+name\s*=\s*["']viewport["']`)
	faviconRE  = regexp.MustCompile(`<link[^>]+rel\s*=\s*["'][^"']*icon[^"']*["']`)
	emailRE    = regexp.MustCompile(`[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	mailtoRE   = regexp.MustCompile(`href\s*=\s*["']mailto:`)
	// UK phone shapes: 01xxx, 02xxx, 03xxx, 07xxx, +44 …. Conservative
	// matcher — anything caught here is almost certainly a phone; false
	// negatives are fine (the qualitative audit can re-confirm).
	phoneUKRE = regexp.MustCompile(`(\+44\s?[\d\s]{9,12}|0[123478]\d{2,3}[\s\-]?\d{3}[\s\-]?\d{3,4})`)
)

// PageSpeed Insights response — only the fields we use.
type pagespeedResponse struct {
	LighthouseResult struct {
		Categories struct {
			Performance   psiCategory `json:"performance"`
			Accessibility psiCategory `json:"accessibility"`
			SEO           psiCategory `json:"seo"`
		} `json:"categories"`
	} `json:"lighthouseResult"`
}

type psiCategory struct {
	Score float64 `json:"score"`
}

// DefaultHTTPTimeout is the http.Client timeout the audit Lambda
// will configure when wiring a real *http.Client at startup.
const DefaultHTTPTimeout = 30 * time.Second
