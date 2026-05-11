// Package googleplaces implements the Google Places (New) Text
// Search discovery provider. Better domain coverage than Companies
// House (Places carries website URIs directly) — at $0.017 per
// call it's the paid path, so the discover Lambda wraps invocations
// in pkg/cost.WithCostCap (iter 1.3).
//
// Auth: X-Goog-Api-Key header. The API key is supplied via the
// GOOGLE_PLACES_API_KEY env var at Lambda startup, populated from
// SSM Parameter Store by the platform layer.
//
// Endpoint: POST https://places.googleapis.com/v1/places:searchText
// Docs: https://developers.google.com/maps/documentation/places/web-service/text-search
package googleplaces

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

const (
	providerID  = "google-places"
	defaultBase = "https://places.googleapis.com"
	// $0.017 per Place Search call (Text Search) — see
	// .ralph/specs/05-capacity-and-cost.md § Cost model.
	costPerCallUSD = 0.017
)

// HTTPDoer is the subset of *http.Client we depend on. Tests inject
// a fake to avoid real HTTP.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Provider struct {
	APIKey  string
	BaseURL string   // empty → defaultBase
	HTTP    HTTPDoer // nil → http.Client with 30s timeout
}

func (p *Provider) ID() string              { return providerID }
func (p *Provider) CostPerCallUSD() float64 { return costPerCallUSD }

// Find queries Places Text Search with the vertical + location +
// include-keywords joined as the text query. The X-Goog-FieldMask
// header restricts the response to the fields we use — Google
// bills less for narrower field masks, so this is both a privacy
// and a cost optimisation.
func (p *Provider) Find(ctx context.Context, req discovery.FindRequest, cap int) ([]discovery.DiscoveredBusiness, error) {
	if p.APIKey == "" {
		return nil, errors.New("googleplaces: APIKey is required")
	}
	q := buildQuery(req)
	if q == "" {
		return nil, errors.New("googleplaces: empty query — vertical or location required")
	}

	base := p.BaseURL
	if base == "" {
		base = defaultBase
	}
	pageSize := cap
	if pageSize <= 0 || pageSize > 20 {
		// Google Places Text Search caps each response at 20.
		pageSize = 20
	}

	bodyJSON, err := json.Marshal(map[string]any{
		"textQuery":    q,
		"pageSize":     pageSize,
		"languageCode": "en",
		"regionCode":   "GB",
	})
	if err != nil {
		return nil, fmt.Errorf("googleplaces: marshal body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, base+"/v1/places:searchText", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("googleplaces: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Goog-Api-Key", p.APIKey)
	// FieldMask is documented as required for the New Places API
	// — Google rejects with 400 INVALID_ARGUMENT without it.
	httpReq.Header.Set(
		"X-Goog-FieldMask",
		"places.id,places.displayName,places.websiteUri,places.formattedAddress",
	)

	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("googleplaces: POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("googleplaces: API key rejected (%d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("googleplaces: rate-limited (429) — discover Lambda should back off")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("googleplaces: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var page searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("googleplaces: decode response: %w", err)
	}

	return convert(page.Places, req.Vertical, req.Location), nil
}

func buildQuery(req discovery.FindRequest) string {
	var parts []string
	if req.Vertical != "" {
		parts = append(parts, req.Vertical)
	}
	if req.Location != "" {
		parts = append(parts, "in", req.Location)
	}
	parts = append(parts, req.IncludeKeywords...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func convert(places []place, vertical, location string) []discovery.DiscoveredBusiness {
	out := make([]discovery.DiscoveredBusiness, 0, len(places))
	for _, pl := range places {
		name := strings.TrimSpace(pl.DisplayName.Text)
		if name == "" {
			continue
		}
		domain := normaliseDomain(pl.WebsiteURI)
		// Confidence: Places' precision is generally high. A
		// place with no website drops to 0.6 (the audit Lambda
		// will struggle to fetch it).
		confidence := 0.9
		if domain == "" {
			confidence = 0.6
		}
		out = append(out, discovery.DiscoveredBusiness{
			Name:     name,
			Domain:   domain,
			Vertical: vertical,
			Location: location,
			Source:   providerID,
			SourceRefs: map[string]string{
				"googlePlaceId":    pl.ID,
				"formattedAddress": pl.FormattedAddress,
			},
			Confidence: confidence,
		})
	}
	return out
}

// normaliseDomain strips scheme + trailing path so downstream
// consumers can use the value directly as the business `domain`.
// Returns empty for invalid input rather than the unmodified URL —
// don't let bad data leak into the domain field.
func normaliseDomain(websiteURI string) string {
	if websiteURI == "" {
		return ""
	}
	// Cheap parse — we just want the host.
	s := websiteURI
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimPrefix(s, "www."))
	if s == "" || !strings.Contains(s, ".") {
		return ""
	}
	return s
}

// Response shapes — only the fields we ask for in the FieldMask.
type searchResponse struct {
	Places []place `json:"places"`
}

type place struct {
	ID               string    `json:"id"`
	DisplayName      placeName `json:"displayName"`
	WebsiteURI       string    `json:"websiteUri,omitempty"`
	FormattedAddress string    `json:"formattedAddress,omitempty"`
}

type placeName struct {
	Text         string `json:"text"`
	LanguageCode string `json:"languageCode,omitempty"`
}
