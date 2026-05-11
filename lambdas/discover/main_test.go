package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

// --- fakes ---------------------------------------------------------------

type fakeDDB struct {
	// pkOfExistingDomain → "DOMAIN#foo.com" entries already in gsi3.
	existingDomains map[string]bool
	putInputs       []*dynamodb.PutItemInput
	putErr          error
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putInputs = append(f.putInputs, in)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}

func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pk, _ := in.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS)
	if pk != nil && f.existingDomains[pk.Value] {
		return &dynamodb.QueryOutput{
			Items: []map[string]dtypes.AttributeValue{
				{"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#existing"}},
			},
		}, nil
	}
	return &dynamodb.QueryOutput{}, nil
}

// --- fake provider -------------------------------------------------------

type fakeProvider struct {
	id      string
	cost    float64
	results []discovery.DiscoveredBusiness
	err     error
	calls   int32
}

func (p *fakeProvider) ID() string              { return p.id }
func (p *fakeProvider) CostPerCallUSD() float64 { return p.cost }
func (p *fakeProvider) Find(_ context.Context, _ discovery.FindRequest, _ int) ([]discovery.DiscoveredBusiness, error) {
	atomic.AddInt32(&p.calls, 1)
	if p.err != nil {
		return nil, p.err
	}
	return p.results, nil
}

// --- setup -----------------------------------------------------------

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	fake := &fakeDDB{existingDomains: map[string]bool{}}
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return fake
}

func validProfile(id string) targeting.Profile {
	return targeting.Profile{
		ID:       id,
		Vertical: "accountants",
		Location: "Manchester",
		Enabled:  true,
	}
}

// publishCapture captures emitted envelopes for assertion.
type publishCapture struct {
	envs []events.Envelope[BusinessFoundDetail]
	err  error
}

func (pc *publishCapture) publish(_ context.Context, env events.Envelope[BusinessFoundDetail]) error {
	pc.envs = append(pc.envs, env)
	return pc.err
}

func baseDeps(profiles []targeting.Profile, providers []discovery.Provider, pc *publishCapture) runDeps {
	idCounter := 0
	return runDeps{
		ListProfiles: func(_ context.Context) ([]targeting.Profile, error) { return profiles, nil },
		Providers:    providers,
		Publish:      pc.publish,
		BudgetUSD:    func(_ context.Context, _ string) (float64, error) { return 100, nil },
		Now:          func() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) },
		NewBizID:     func() string { idCounter++; return "biz-" + intStr(idCounter) },
	}
}

func intStr(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{digits[i%10]}, out...)
		i /= 10
	}
	return string(out)
}

// --- happy path ---------------------------------------------------------

func TestRun_PersistsAndPublishesForNewBusinesses(t *testing.T) {
	fake := reset(t)
	prov := &fakeProvider{id: "csv", cost: 0, results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk", Source: "csv", Confidence: 1.0},
		{Name: "Beta", Domain: "beta.co.uk", Source: "csv", Confidence: 1.0},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{prov}, pc)

	if err := run(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fake.putInputs) != 2 {
		t.Fatalf("expected 2 PutItem, got %d", len(fake.putInputs))
	}
	if len(pc.envs) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(pc.envs))
	}
	if pc.envs[0].EventName != "business.found" {
		t.Errorf("eventName=%q, want business.found", pc.envs[0].EventName)
	}
	if pc.envs[0].Detail.Domain != "acme.co.uk" {
		t.Errorf("detail.domain=%q", pc.envs[0].Detail.Domain)
	}
	if pc.envs[0].Detail.ProfileID != "p1" {
		t.Errorf("detail.profileId=%q, want p1", pc.envs[0].Detail.ProfileID)
	}
}

func TestRun_SkipsDisabledProfiles(t *testing.T) {
	reset(t)
	enabled := validProfile("on")
	disabled := validProfile("off")
	disabled.Enabled = false

	prov := &fakeProvider{id: "csv", results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk", Source: "csv"},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{enabled, disabled}, []discovery.Provider{prov}, pc)

	_ = run(context.Background(), deps)
	if atomic.LoadInt32(&prov.calls) != 1 {
		t.Errorf("provider called %d times, want 1 (one enabled profile)", prov.calls)
	}
}

func TestRun_DedupesAlreadyKnownDomain(t *testing.T) {
	fake := reset(t)
	fake.existingDomains["DOMAIN#acme.co.uk"] = true

	prov := &fakeProvider{id: "csv", results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk", Source: "csv"},
		{Name: "Beta", Domain: "beta.co.uk", Source: "csv"},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{prov}, pc)

	_ = run(context.Background(), deps)
	if len(fake.putInputs) != 1 {
		t.Errorf("expected 1 PutItem (beta only), got %d", len(fake.putInputs))
	}
	if len(pc.envs) != 1 {
		t.Errorf("expected 1 event, got %d", len(pc.envs))
	}
	if pc.envs[0].Detail.Domain != "beta.co.uk" {
		t.Errorf("event domain=%q, want beta", pc.envs[0].Detail.Domain)
	}
}

func TestRun_SkipsBusinessesWithoutDomain(t *testing.T) {
	fake := reset(t)
	prov := &fakeProvider{id: "companies-house", results: []discovery.DiscoveredBusiness{
		{Name: "No-Web Ltd", Domain: "", Source: "companies-house"},
		{Name: "Web Co", Domain: "web.co.uk", Source: "companies-house"},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{prov}, pc)

	_ = run(context.Background(), deps)
	if len(fake.putInputs) != 1 {
		t.Errorf("expected 1 PutItem (web.co.uk only), got %d", len(fake.putInputs))
	}
}

func TestRun_LowercasesDomainBeforeDedup(t *testing.T) {
	fake := reset(t)
	fake.existingDomains["DOMAIN#acme.co.uk"] = true

	prov := &fakeProvider{id: "csv", results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "  ACME.CO.UK ", Source: "csv"}, // case + whitespace
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{prov}, pc)

	_ = run(context.Background(), deps)
	if len(fake.putInputs) != 0 {
		t.Errorf("dedup didn't lowercase domain; got %d puts", len(fake.putInputs))
	}
}

// --- cost cap -----------------------------------------------------------

func TestRun_PlacesCapExceeded_SkipsRestOfRun(t *testing.T) {
	reset(t)
	prov := &fakeProvider{id: "google-places", cost: 0.017, results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk"},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1"), validProfile("p2")}, []discovery.Provider{prov}, pc)
	// Budget of 0 → cap.WithCostCap returns ErrBudgetCapExceeded
	// on the first attempt.
	deps.BudgetUSD = func(_ context.Context, _ string) (float64, error) { return 0.001, nil }

	if err := run(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Provider should be called at most once, then the run skips
	// it for the second profile.
	if calls := atomic.LoadInt32(&prov.calls); calls > 1 {
		t.Errorf("provider called %d times after cap exceeded, want ≤1", calls)
	}
}

// --- provider error isolation ------------------------------------------

func TestRun_OneProviderFailingDoesNotBlockOthers(t *testing.T) {
	reset(t)
	bad := &fakeProvider{id: "google-places", err: errors.New("places down")}
	good := &fakeProvider{id: "csv", results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk", Source: "csv"},
	}}
	pc := &publishCapture{}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{bad, good}, pc)

	if err := run(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(pc.envs) != 1 {
		t.Errorf("expected 1 event from the good provider, got %d", len(pc.envs))
	}
}

// --- publish failure ---------------------------------------------------

func TestRun_PublishFailure_DoesNotRollbackDDBWrite(t *testing.T) {
	fake := reset(t)
	prov := &fakeProvider{id: "csv", results: []discovery.DiscoveredBusiness{
		{Name: "Acme", Domain: "acme.co.uk", Source: "csv"},
	}}
	pc := &publishCapture{err: errors.New("eb down")}
	deps := baseDeps([]targeting.Profile{validProfile("p1")}, []discovery.Provider{prov}, pc)

	_ = run(context.Background(), deps)
	// Business row was still written — downstream consumers can
	// recover by replaying the BUSINESS#STATUS#new gsi1.
	if len(fake.putInputs) != 1 {
		t.Errorf("PutItem was rolled back on publish failure: %d", len(fake.putInputs))
	}
}

// --- sanity: ListProfiles error surfaces -------------------------------

func TestRun_ListProfilesError_Returns(t *testing.T) {
	reset(t)
	deps := baseDeps(nil, nil, &publishCapture{})
	deps.ListProfiles = func(_ context.Context) ([]targeting.Profile, error) {
		return nil, errors.New("ddb down")
	}
	err := run(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error from ListProfiles, got nil")
	}
}

// Avoid unused-import in this file when cost package isn't referenced
// directly — the import is real (handler uses cost.WithCostCap).
var _ = cost.ErrBudgetCapExceeded
