// Package discovery defines the contract every discovery provider
// (Companies House, Google Places, CSV uploads, future Yell/Bing
// providers) implements. The discover Lambda (iter 1.3) iterates
// every enabled provider, calls Find against the current targeting
// profile, normalises results to DiscoveredBusiness, and publishes
// `business.found` events.
//
// Schema reflects .ralph/specs/06-discovery-and-compliance.md §
// "Discovery providers" — domain may be null for Companies House
// (the CH record doesn't carry website; the audit Lambda resolves
// domains later via a separate lookup).
package discovery

import "context"

// DiscoveredBusiness is the normalised shape every provider returns.
// Source-specific identifiers go in SourceRefs (e.g.
// `companiesHouseNumber`, `googlePlaceId`) so downstream consumers
// can resolve back to the origin record without colonising the
// top-level shape.
type DiscoveredBusiness struct {
	Name       string            `json:"name"`
	Domain     string            `json:"domain,omitempty"`
	Location   string            `json:"location,omitempty"`
	Vertical   string            `json:"vertical,omitempty"`
	Source     string            `json:"source"`
	SourceRefs map[string]string `json:"sourceRefs,omitempty"`
	Confidence float64           `json:"confidence"`
}

// Provider is the contract for one source of business records. Find
// is called by the discover Lambda once per scheduled run, per
// enabled TargetingProfile. Implementations should:
//
//   - Return at most `cap` records — the Lambda enforces a daily
//     cap via pkg/cost ledger separately; cap here is per-CALL.
//   - Use pkg/politefetch for any outbound HTTP so robots.txt +
//     rate limiting are honoured.
//   - Wrap paid calls in pkg/cost.WithCostCap when CostPerCall > 0.
//
// Returning a partial result + nil error is acceptable when the
// provider hit its internal limit; the caller decides whether to
// retry next cycle.
type Provider interface {
	// ID returns a stable identifier matching the documented allowed
	// values in 06-discovery-and-compliance.md.
	ID() string
	// CostPerCallUSD is the per-call cost in USD. Used by the
	// discover Lambda to wrap the call in pkg/cost.WithCostCap.
	// Free providers return 0.
	CostPerCallUSD() float64
	// Find returns up to `cap` businesses matching the targeting
	// profile. Context cancellation must abort the in-flight call.
	Find(ctx context.Context, profile FindRequest, cap int) ([]DiscoveredBusiness, error)
}

// FindRequest is the subset of TargetingProfile a provider needs to
// query. The full Profile lives in pkg/targeting and would create a
// package cycle if imported here — Profile.ToFindRequest() (added
// in iter 1.3 alongside the discover Lambda wiring) bridges the gap.
type FindRequest struct {
	Vertical        string
	Location        string
	IncludeKeywords []string
	ExcludeKeywords []string
}
