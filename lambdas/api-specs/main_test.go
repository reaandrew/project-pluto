package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/feedback"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// --- fakes -------------------------------------------------------------

type fakeDDB struct {
	items   map[string]map[string]dtypes.AttributeValue
	puts    []*dynamodb.PutItemInput
	queries []*dynamodb.QueryInput
}

func newFakeDDB() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]dtypes.AttributeValue{}}
}

func keyOf(pk, sk string) string { return pk + "|" + sk }

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.puts = append(f.puts, in)
	pk := in.Item["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Item["sk"].(*dtypes.AttributeValueMemberS).Value
	f.items[keyOf(pk, sk)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Key["sk"].(*dtypes.AttributeValueMemberS).Value
	item := f.items[keyOf(pk, sk)]
	return &dynamodb.GetItemOutput{Item: item}, nil
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
	f.queries = append(f.queries, in)
	pkAttr := in.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	prefixAttr, _ := in.ExpressionAttributeValues[":prefix"].(*dtypes.AttributeValueMemberS)
	var hits []map[string]dtypes.AttributeValue
	for k, item := range f.items {
		parts := strings.SplitN(k, "|", 2)
		if parts[0] != pkAttr {
			continue
		}
		if prefixAttr != nil && !strings.HasPrefix(parts[1], prefixAttr.Value) {
			continue
		}
		hits = append(hits, item)
	}
	// Reverse-sort by sk so newest-first when ScanIndexForward=false.
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
			hits[i], hits[j] = hits[j], hits[i]
		}
	}
	return &dynamodb.QueryOutput{Items: hits, Count: int32(len(hits))}, nil
}

type fakeEB struct {
	puts []*eventbridge.PutEventsInput
}

func (f *fakeEB) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.puts = append(f.puts, in)
	return &eventbridge.PutEventsOutput{}, nil
}

// --- harness -----------------------------------------------------------

func setup(t *testing.T) (*fakeDDB, *fakeEB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("EVENT_BUS_NAME", "test-bus")
	d := newFakeDDB()
	ddb.SetClient(d)
	eb := &fakeEB{}
	cachedPublisher = pkgevents.NewPublisherWithClient(eb, "test-bus")
	feedback.SetNowFunc(func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) })
	feedback.SetIDFunc(func() string { return "fb-1" })
	nowFunc = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	randomHexFn = func(int) string { return "etag-test" }
	t.Cleanup(func() {
		ddb.SetClient(nil)
		cachedPublisher = nil
		feedback.SetNowFunc(func() time.Time { return time.Now().UTC() })
		feedback.SetIDFunc(func() string { return defaultRandomHex(16) })
		nowFunc = func() time.Time { return time.Now().UTC() }
		randomHexFn = defaultRandomHex
	})
	return d, eb
}

func seedBusiness(d *fakeDDB) {
	d.items[keyOf("BUSINESS#biz-1", "PROFILE")] = map[string]dtypes.AttributeValue{
		"pk":       &dtypes.AttributeValueMemberS{Value: "BUSINESS#biz-1"},
		"sk":       &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		"id":       &dtypes.AttributeValueMemberS{Value: "biz-1"},
		"name":     &dtypes.AttributeValueMemberS{Value: "Acme Plumbing"},
		"domain":   &dtypes.AttributeValueMemberS{Value: "acme.co.uk"},
		"vertical": &dtypes.AttributeValueMemberS{Value: "trades"},
		"location": &dtypes.AttributeValueMemberS{Value: "Manchester"},
		"status":   &dtypes.AttributeValueMemberS{Value: "qualified"},
	}
}

func validContent() schemas.SpecV1 {
	return schemas.SpecV1{
		Brand: schemas.SpecBrand{
			Tone: "plain", Positioning: "Local.",
			Palette: schemas.SpecPalette{Primary: "#0F4C81", NeutralDark: "#000", NeutralLight: "#fff"},
		},
		Page: schemas.SpecPage{Sections: []schemas.SpecSection{
			{Type: schemas.SectionHero, Headline: "Hi", Subheadline: "Hello",
				PrimaryCta: &schemas.SpecCTA{Label: "Call", Action: "call"}},
			{Type: schemas.SectionServices, Title: "Services", Items: []schemas.SpecSubItem{
				{Name: "a", OneLine: "b"}, {Name: "c", OneLine: "d"}, {Name: "e", OneLine: "f"},
			}},
			{Type: schemas.SectionAbout, Paragraph: "About us."},
			{Type: schemas.SectionContact, Phone: "0161 234 5678"},
		}},
		SEO: schemas.SpecSEO{Title: "Acme", Description: "Acme Plumbers."},
		Constraints: schemas.SpecConstraints{
			DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
		},
	}
}

func seedSpec(d *fakeDDB, status string) {
	row := SpecRow{
		PK: "BUSINESS#biz-1", SK: "SPEC#spec-1", Type: "Spec", ID: "spec-1",
		Version: 1, Status: status,
		Content: validContent(),
		ModelID: "anthropic.claude-sonnet-4-6", PromptID: "spec.v1",
		CreatedAt: "2026-05-14T11:00:00Z", UpdatedAt: "2026-05-14T11:00:00Z",
		Etag: "seed",
	}
	item, _ := attrMarshal(row)
	d.items[keyOf(row.PK, row.SK)] = item
}

func attrMarshal(row SpecRow) (map[string]dtypes.AttributeValue, error) {
	return attributevalue.MarshalMap(row)
}

func makeReq(method, path string, body string, params map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{
		Body:           body,
		PathParameters: params,
	}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = path
	// Inject an operator JWT.
	r.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
			Claims: map[string]string{
				"cognito:groups": "[operator]",
				"sub":            "cog-test-user",
			},
		},
	}
	return r
}

// --- 403 / 404 / 400 / 405 -------------------------------------------

func TestHandle_NotOperator_Forbidden(t *testing.T) {
	setup(t)
	req := events.APIGatewayV2HTTPRequest{
		PathParameters: map[string]string{"businessId": "biz-1"},
	}
	req.RequestContext.HTTP.Method = "GET"
	req.RequestContext.HTTP.Path = "/candidates/biz-1"
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("StatusCode = %d, want 403", resp.StatusCode)
	}
}

func TestHandle_GetCandidate_404WhenBusinessMissing(t *testing.T) {
	setup(t)
	req := makeReq("GET", "/candidates/missing", "", map[string]string{"businessId": "missing"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

func TestHandle_WrongMethod_405(t *testing.T) {
	setup(t)
	req := makeReq("DELETE", "/candidates/biz-1", "", map[string]string{"businessId": "biz-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 405 {
		t.Errorf("StatusCode = %d, want 405", resp.StatusCode)
	}
}

// --- GET /candidates/{businessId} ------------------------------------

func TestHandle_GetCandidate_ReturnsBusinessAndLatestSpec(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedSpec(d, "draft")

	req := makeReq("GET", "/candidates/biz-1", "", map[string]string{"businessId": "biz-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d (%s)", resp.StatusCode, resp.Body)
	}
	var got candidateResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Business.Name != "Acme Plumbing" {
		t.Errorf("business drift: %+v", got.Business)
	}
	if got.Spec == nil || got.Spec.ID != "spec-1" || got.Spec.Status != "draft" {
		t.Errorf("spec drift: %+v", got.Spec)
	}
}

func TestHandle_GetCandidate_NoSpecYet_ReturnsNilSpec(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)

	req := makeReq("GET", "/candidates/biz-1", "", map[string]string{"businessId": "biz-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d (%s)", resp.StatusCode, resp.Body)
	}
	var got candidateResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Spec != nil {
		t.Errorf("expected nil spec when none exists, got %+v", got.Spec)
	}
}

// --- PATCH /specs/{id} ------------------------------------------------

func TestHandle_PatchSpec_EditsContentAndBumpsVersion(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedSpec(d, "draft")

	edited := validContent()
	edited.Brand.Tone = "warmer, friendlier"
	body := patchSpecBody{Content: edited, Notes: "Tone too cold"}
	bodyJSON, _ := json.Marshal(body)

	req := makeReq("PATCH", "/candidates/biz-1/specs/spec-1", string(bodyJSON),
		map[string]string{"businessId": "biz-1", "specId": "spec-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d (%s)", resp.StatusCode, resp.Body)
	}
	var updated SpecRow
	if err := json.Unmarshal([]byte(resp.Body), &updated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("version = %d, want 2", updated.Version)
	}
	if updated.Content.Brand.Tone != "warmer, friendlier" {
		t.Errorf("content not updated: %+v", updated.Content.Brand)
	}
	if updated.Status != "draft" {
		t.Errorf("status should stay draft, got %q", updated.Status)
	}
	if updated.Etag == "seed" {
		t.Errorf("etag should rotate")
	}
	// feedback.captured emitted.
	if len(eb.puts) != 1 {
		t.Fatalf("expected 1 PutEvents call, got %d", len(eb.puts))
	}
	if dt := *eb.puts[0].Entries[0].DetailType; dt != "feedback.captured" {
		t.Errorf("expected feedback.captured event, got %q", dt)
	}
}

func TestHandle_PatchSpec_RejectsInvalidContent(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedSpec(d, "draft")

	bad := validContent()
	bad.Constraints.DoNotInventTestimonials = false
	body, _ := json.Marshal(patchSpecBody{Content: bad})

	req := makeReq("PATCH", "/candidates/biz-1/specs/spec-1", string(body),
		map[string]string{"businessId": "biz-1", "specId": "spec-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 on invalid content, got %d (%s)", resp.StatusCode, resp.Body)
	}
}

func TestHandle_PatchSpec_404WhenSpecMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	// No spec seeded.

	body, _ := json.Marshal(patchSpecBody{Content: validContent()})
	req := makeReq("PATCH", "/candidates/biz-1/specs/spec-x", string(body),
		map[string]string{"businessId": "biz-1", "specId": "spec-x"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// --- POST /approve + /reject -----------------------------------------

func TestHandle_Approve_FlipsStatusAndEmitsTwoEvents(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedSpec(d, "draft")

	req := makeReq("POST", "/candidates/biz-1/specs/spec-1/approve", `{}`,
		map[string]string{"businessId": "biz-1", "specId": "spec-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d (%s)", resp.StatusCode, resp.Body)
	}
	var updated SpecRow
	if err := json.Unmarshal([]byte(resp.Body), &updated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if updated.Status != "approved" {
		t.Errorf("status = %q, want approved", updated.Status)
	}
	if updated.ApprovedBy != "cog-test-user" {
		t.Errorf("approvedBy = %q, want cog-test-user", updated.ApprovedBy)
	}
	if updated.ApprovedAt == "" {
		t.Errorf("approvedAt should be set")
	}
	// Two events: spec.approved + feedback.captured.
	if len(eb.puts) != 2 {
		t.Fatalf("expected 2 PutEvents calls (spec.approved + feedback.captured), got %d", len(eb.puts))
	}
	got := []string{}
	for _, p := range eb.puts {
		got = append(got, *p.Entries[0].DetailType)
	}
	if !containsString(got, "spec.approved") || !containsString(got, "feedback.captured") {
		t.Errorf("expected both spec.approved + feedback.captured; got %v", got)
	}
}

func TestHandle_Reject_FlipsStatusAndEmitsTwoEvents(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedSpec(d, "draft")

	body, _ := json.Marshal(approveRejectBody{Notes: "tone is wrong"})
	req := makeReq("POST", "/candidates/biz-1/specs/spec-1/reject", string(body),
		map[string]string{"businessId": "biz-1", "specId": "spec-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d (%s)", resp.StatusCode, resp.Body)
	}
	var updated SpecRow
	if err := json.Unmarshal([]byte(resp.Body), &updated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if updated.Status != "rejected" {
		t.Errorf("status = %q, want rejected", updated.Status)
	}
	if len(eb.puts) != 2 {
		t.Errorf("expected 2 events (spec.rejected + feedback.captured), got %d", len(eb.puts))
	}
}

func TestHandle_Approve_409OnNonDraft(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedSpec(d, "approved") // already approved

	req := makeReq("POST", "/candidates/biz-1/specs/spec-1/approve", `{}`,
		map[string]string{"businessId": "biz-1", "specId": "spec-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 when spec is not draft, got %d", resp.StatusCode)
	}
}

func TestHandle_Reject_404WhenSpecMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)

	req := makeReq("POST", "/candidates/biz-1/specs/spec-x/reject", `{}`,
		map[string]string{"businessId": "biz-1", "specId": "spec-x"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// --- helpers ----------------------------------------------------------

func containsString(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}
