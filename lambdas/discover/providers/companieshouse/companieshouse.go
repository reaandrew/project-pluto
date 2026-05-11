// Package companieshouse implements the Companies House Search
// Companies discovery provider. UK only — pulls company-name +
// registered-office + companies-house-number from the free CH API.
//
// Domains are NOT in Companies House — `Domain` on the returned
// records is always empty. The downstream audit Lambda (iter 2)
// resolves a domain via a separate lookup (typically Google CSE
// over the company name).
//
// Auth: HTTP Basic — username = API key, password = empty. The
// API key is supplied via the COMPANIES_HOUSE_API_KEY env var at
// Lambda startup, populated from SSM Parameter Store by the
// platform layer (iter 1.3's discover Lambda will wire it).
//
// Rate limit: 600 requests / 5 minutes per key (CH's documented
// limit). Hot path enforcement is the discover Lambda's job; we
// just call the API and surface a clear error on 429.
package companieshouse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

const (
	providerID  = "companies-house"
	defaultBase = "https://api.company-information.service.gov.uk"
)

// HTTPDoer is the subset of *http.Client we depend on. Tests inject
// a fake to avoid real HTTP.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Provider implements discovery.Provider against the Companies House
// Search Companies API.
type Provider struct {
	APIKey  string
	BaseURL string   // empty → defaultBase
	HTTP    HTTPDoer // nil → http.Client with 30s timeout
}

func (p *Provider) ID() string              { return providerID }
func (p *Provider) CostPerCallUSD() float64 { return 0 } // free API

// Find queries CH for company-name matches against the targeting
// profile. Vertical isn't a directly-queryable CH field, so we
// build a free-text query from `vertical + location + include
// keywords` and let the response-side filter run downstream.
// `cap` is honoured as the max items_per_page (CH supports up to
// 100 per page).
func (p *Provider) Find(ctx context.Context, req discovery.FindRequest, cap int) ([]discovery.DiscoveredBusiness, error) {
	if p.APIKey == "" {
		return nil, errors.New("companieshouse: APIKey is required")
	}
	q := buildQuery(req)
	if q == "" {
		return nil, errors.New("companieshouse: empty query — vertical or location required")
	}

	base := p.BaseURL
	if base == "" {
		base = defaultBase
	}
	itemsPerPage := cap
	if itemsPerPage <= 0 || itemsPerPage > 100 {
		itemsPerPage = 100
	}

	u, err := url.Parse(base + "/search/companies")
	if err != nil {
		return nil, fmt.Errorf("companieshouse: parsing URL: %w", err)
	}
	qs := u.Query()
	qs.Set("q", q)
	qs.Set("items_per_page", fmt.Sprintf("%d", itemsPerPage))
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("companieshouse: building request: %w", err)
	}
	// Basic auth: APIKey + empty password — that's the documented
	// CH auth pattern.
	httpReq.Header.Set("Authorization", "Basic "+basicAuthEncode(p.APIKey))
	httpReq.Header.Set("Accept", "application/json")

	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("companieshouse: GET %s: %w", u.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("companieshouse: API key rejected (%d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("companieshouse: rate-limited (429) — discover Lambda should back off")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("companieshouse: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var page searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("companieshouse: decoding response: %w", err)
	}

	return convert(page.Items, req.Vertical, req.Location), nil
}

// buildQuery composes the free-text query string CH searches against.
// Vertical and location are most signal-bearing; include-keywords
// add disambiguation when supplied.
func buildQuery(req discovery.FindRequest) string {
	var parts []string
	if req.Vertical != "" {
		parts = append(parts, req.Vertical)
	}
	if req.Location != "" {
		parts = append(parts, req.Location)
	}
	parts = append(parts, req.IncludeKeywords...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// convert maps the CH response into DiscoveredBusiness records.
// vertical + location come from the targeting profile (CH doesn't
// classify) so all results from one query share those fields.
func convert(items []searchItem, vertical, location string) []discovery.DiscoveredBusiness {
	out := make([]discovery.DiscoveredBusiness, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Title)
		if name == "" {
			continue
		}
		// Confidence: CH returns lots of historical dissolved
		// companies. Active = high confidence, anything else
		// drops to 0.6 (qualifier can re-rank).
		confidence := 0.6
		if strings.EqualFold(it.CompanyStatus, "active") {
			confidence = 0.85
		}
		out = append(out, discovery.DiscoveredBusiness{
			Name:     name,
			Domain:   "", // CH doesn't carry websites
			Vertical: vertical,
			Location: location,
			Source:   providerID,
			SourceRefs: map[string]string{
				"companiesHouseNumber": it.CompanyNumber,
			},
			Confidence: confidence,
		})
	}
	return out
}

// basicAuthEncode produces the base64 of "<apiKey>:" — APIKey as
// username, empty password. Per CH docs.
func basicAuthEncode(apiKey string) string {
	return base64.StdEncoding.EncodeToString([]byte(apiKey + ":"))
}

// Response shapes from the CH Search Companies API — only the
// fields we use are mapped; extras parse as no-ops.
type searchResponse struct {
	Items []searchItem `json:"items"`
}

type searchItem struct {
	Title         string `json:"title"`
	CompanyNumber string `json:"company_number"`
	CompanyStatus string `json:"company_status"`
}
