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
	return &dynamodb.GetItemOutput{Item: f.items[keyOf(pk, sk)]}, nil
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
	feedback.SetNowFunc(func() time.Time { return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC) })
	feedback.SetIDFunc(func() string { return "fb-1" })
	nowFunc = func() time.Time { return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC) }
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

func seedWebsite(d *fakeDDB, status string) {
	row := WebsiteRow{
		PK: "BUSINESS#biz-1", SK: "WEBSITE#web-1", Type: "Website", ID: "web-1",
		SpecID: "spec-1", R2Prefix: "sites/web-1/", Status: status,
		PreviewURL:              "https://previews.example.com/sites/web-1",
		Screenshots:             map[string]string{"desktop": "https://previews.example.com/screenshots/web-1/desktop.png"},
		PasscodeHash:            "SECRET_HASH_DO_NOT_LEAK",
		PasscodeCipher:          "SECRET_CIPHER_DO_NOT_LEAK",
		PasscodeRevealableUntil: 1799999999,
		CreatedAt:               "2026-05-15T11:00:00Z", UpdatedAt: "2026-05-15T11:00:00Z",
		Etag: "seed",
	}
	item, _ := attributevalue.MarshalMap(row)
	d.items[keyOf(row.PK, row.SK)] = item
}

func makeReq(method, path, body string, params map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{Body: body, PathParameters: params}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = path
	r.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
			Claims: map[string]string{"cognito:groups": "[operator]", "sub": "cog-test-user"},
		},
	}
	return r
}

// --- auth + routing ----------------------------------------------------

func TestHandle_NotOperator_Forbidden(t *testing.T) {
	setup(t)
	req := events.APIGatewayV2HTTPRequest{PathParameters: map[string]string{"businessId": "biz-1"}}
	req.RequestContext.HTTP.Method = "GET"
	req.RequestContext.HTTP.Path = "/candidates/biz-1/website"
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("StatusCode = %d, want 403", resp.StatusCode)
	}
}

func TestHandle_Get_404WhenBusinessMissing(t *testing.T) {
	setup(t)
	req := makeReq("GET", "/candidates/missing/website", "", map[string]string{"businessId": "missing"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

func TestHandle_Get_WebsiteNilWhenNonePublished(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	req := makeReq("GET", "/candidates/biz-1/website", "", map[string]string{"businessId": "biz-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	var got websiteResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Website != nil {
		t.Errorf("Website should be nil when none published, got %+v", got.Website)
	}
	if got.Business.ID != "biz-1" {
		t.Errorf("business not returned: %+v", got.Business)
	}
}

func TestHandle_Get_SanitisesPasscodeMaterial(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	req := makeReq("GET", "/candidates/biz-1/website", "", map[string]string{"businessId": "biz-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(resp.Body, "SECRET_HASH_DO_NOT_LEAK") || strings.Contains(resp.Body, "SECRET_CIPHER_DO_NOT_LEAK") {
		t.Fatalf("passcode material leaked in GET response: %s", resp.Body)
	}
	var got websiteResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Website == nil || got.Website.PreviewURL != "https://previews.example.com/sites/web-1" {
		t.Fatalf("website view drift: %+v", got.Website)
	}
	if got.Website.Screenshots["desktop"] == "" {
		t.Errorf("screenshots not surfaced")
	}
}

func TestHandle_Approve_HappyPath(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	req := makeReq("POST", "/candidates/biz-1/website/web-1/approve",
		`{"notes":"looks great"}`,
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (body %s)", resp.StatusCode, resp.Body)
	}
	var view websiteView
	if err := json.Unmarshal([]byte(resp.Body), &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if view.Status != "approved" || view.ApprovedBy != "cog-test-user" {
		t.Errorf("status/approvedBy drift: %+v", view)
	}
	// website.approved + feedback.captured both published.
	var sawApproved, sawFeedback bool
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			switch *e.DetailType {
			case "website.approved":
				sawApproved = true
			case "feedback.captured":
				sawFeedback = true
				if strings.Contains(*e.Detail, "SECRET_HASH") || strings.Contains(*e.Detail, "SECRET_CIPHER") {
					t.Errorf("passcode material leaked into feedback event: %s", *e.Detail)
				}
			}
		}
	}
	if !sawApproved || !sawFeedback {
		t.Errorf("expected website.approved + feedback.captured; approved=%v feedback=%v", sawApproved, sawFeedback)
	}
	// Feedback row written, no passcode material on it.
	var sawFeedbackRow bool
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			sawFeedbackRow = true
			if op, ok := item["originalPayload"].(*dtypes.AttributeValueMemberS); ok {
				if strings.Contains(op.Value, "SECRET_HASH") || strings.Contains(op.Value, "SECRET_CIPHER") {
					t.Errorf("passcode material leaked into feedback row: %s", op.Value)
				}
			}
		}
	}
	if !sawFeedbackRow {
		t.Error("no feedback row written")
	}
}

func TestHandle_Reject_HappyPath(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	req := makeReq("POST", "/candidates/biz-1/website/web-1/reject", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (%s)", resp.StatusCode, resp.Body)
	}
	var view websiteView
	_ = json.Unmarshal([]byte(resp.Body), &view)
	if view.Status != "rejected" {
		t.Errorf("status = %q, want rejected", view.Status)
	}
	var sawRejected bool
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if *e.DetailType == "website.rejected_after_review" {
				sawRejected = true
			}
		}
	}
	if !sawRejected {
		t.Error("website.rejected_after_review not published")
	}
}

func TestHandle_Decision_409WhenNotPublished(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d, "approved") // already decided
	req := makeReq("POST", "/candidates/biz-1/website/web-1/approve", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 409 {
		t.Errorf("StatusCode = %d, want 409", resp.StatusCode)
	}
}

func TestHandle_Decision_404WhenWebsiteMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	req := makeReq("POST", "/candidates/biz-1/website/nope/approve", "",
		map[string]string{"businessId": "biz-1", "websiteId": "nope"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

// --- iter 5.6b: regenerate-site ---------------------------------------

func TestHandle_RegenerateSite_HappyPath(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d, "approved")
	req := makeReq("POST", "/candidates/biz-1/website/web-1/regenerate-site",
		`{"notes":"swap the hero"}`,
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (%s)", resp.StatusCode, resp.Body)
	}
	var view websiteView
	_ = json.Unmarshal([]byte(resp.Body), &view)
	if view.Status != "regenerated" {
		t.Errorf("status = %q, want regenerated", view.Status)
	}
	var sawRegen, sawFeedback bool
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			switch *e.DetailType {
			case "website.regenerate.requested":
				sawRegen = true
				if !strings.Contains(*e.Detail, `"specId":"spec-1"`) || !strings.Contains(*e.Detail, `"websiteId":"web-1"`) {
					t.Errorf("regenerate event missing specId/websiteId: %s", *e.Detail)
				}
			case "feedback.captured":
				sawFeedback = true
			}
		}
	}
	if !sawRegen || !sawFeedback {
		t.Errorf("expected website.regenerate.requested + feedback.captured; regen=%v fb=%v", sawRegen, sawFeedback)
	}
	// Sanitisation: no passcode material on the regenerate-site path.
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if strings.Contains(*e.Detail, "SECRET_HASH") || strings.Contains(*e.Detail, "SECRET_CIPHER") {
				t.Fatalf("passcode material leaked into a regenerate-site event: %s", *e.Detail)
			}
		}
	}
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			if op, ok := item["originalPayload"].(*dtypes.AttributeValueMemberS); ok &&
				(strings.Contains(op.Value, "SECRET_HASH") || strings.Contains(op.Value, "SECRET_CIPHER")) {
				t.Fatalf("passcode material leaked into the regenerate-site feedback row: %s", op.Value)
			}
		}
	}
}

func TestHandle_RegenerateSite_404WhenMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	req := makeReq("POST", "/candidates/biz-1/website/nope/regenerate-site", "",
		map[string]string{"businessId": "biz-1", "websiteId": "nope"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

// --- iter 5.6b: regenerate-passcode -----------------------------------

func TestHandle_RegeneratePasscode_HappyPath(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")

	var kvKey, kvVal string
	passcodeOpsProvider = func(context.Context) (*passcodeOps, error) {
		return &passcodeOps{
			Gen: func() (string, error) { return "NEWCODE9", nil },
			// Realistic doubles: a real hash/cipher never contains the
			// cleartext, so the leak-sweep below is meaningful.
			Hash: func(_, _ string) string { return "h4sh3dvalue" },
			Encrypt: func(_ context.Context, _ string) (string, error) {
				return "c1ph3rtext", nil
			},
			KVPut: func(_ context.Context, key, value string, _ map[string]string) error {
				kvKey, kvVal = key, value
				return nil
			},
			Salt: "test-salt",
		}, nil
	}
	t.Cleanup(func() { passcodeOpsProvider = defaultPasscodeOps })

	req := makeReq("POST", "/candidates/biz-1/website/web-1/regenerate-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (%s)", resp.StatusCode, resp.Body)
	}
	// Old KV key overwritten with the NEW hash → old cleartext invalid.
	if kvKey != "passcode:web-1" || kvVal != "h4sh3dvalue" {
		t.Errorf("KV overwrite drift: key=%q val=%q", kvKey, kvVal)
	}
	// Cleartext must not appear anywhere observable.
	if strings.Contains(resp.Body, "NEWCODE9") {
		t.Fatalf("cleartext leaked in response: %s", resp.Body)
	}
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if strings.Contains(*e.Detail, "NEWCODE9") {
				t.Fatalf("cleartext leaked in event: %s", *e.Detail)
			}
		}
	}
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			if op, ok := item["originalPayload"].(*dtypes.AttributeValueMemberS); ok && strings.Contains(op.Value, "NEWCODE9") {
				t.Fatalf("cleartext leaked in feedback row: %s", op.Value)
			}
		}
	}
	// Website row got the new sealed material + cleared revocation.
	row := d.items[keyOf("BUSINESS#biz-1", "WEBSITE#web-1")]
	if row["passcodeHash"].(*dtypes.AttributeValueMemberS).Value != "h4sh3dvalue" {
		t.Errorf("passcodeHash not rotated")
	}
	if row["passcodeCipher"].(*dtypes.AttributeValueMemberS).Value != "c1ph3rtext" {
		t.Errorf("passcodeCipher not rotated")
	}
	// Defence-in-depth: no attribute on the persisted Website row may
	// contain the raw cleartext.
	for attr, av := range row {
		if s, ok := av.(*dtypes.AttributeValueMemberS); ok && strings.Contains(s.Value, "NEWCODE9") {
			t.Fatalf("cleartext leaked into Website row attribute %q: %s", attr, s.Value)
		}
	}
}

func TestHandle_RegeneratePasscode_404WhenMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	req := makeReq("POST", "/candidates/biz-1/website/nope/regenerate-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "nope"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

// --- iter 6.3: reveal-passcode ----------------------------------------

func revealOps(decrypted string, decErr error) func(context.Context) (*passcodeOps, error) {
	return func(context.Context) (*passcodeOps, error) {
		return &passcodeOps{
			Decrypt: func(_ context.Context, _ string) (string, error) {
				if decErr != nil {
					return "", decErr
				}
				return decrypted, nil
			},
		}, nil
	}
}

func TestHandle_RevealPasscode_HappyPath(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published") // PasscodeCipher set, revealableUntil in the future
	passcodeOpsProvider = revealOps("CLEAR123", nil)
	t.Cleanup(func() { passcodeOpsProvider = defaultPasscodeOps })

	req := makeReq("POST", "/candidates/biz-1/website/web-1/reveal-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (%s)", resp.StatusCode, resp.Body)
	}
	// The response body is the ONE sanctioned cleartext channel.
	var got revealResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Passcode != "CLEAR123" {
		t.Errorf("revealed passcode = %q, want CLEAR123", got.Passcode)
	}
	// Cleartext must NOT have leaked into any event or DDB row.
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if strings.Contains(*e.Detail, "CLEAR123") {
				t.Fatalf("cleartext leaked into an event: %s", *e.Detail)
			}
		}
	}
	for k, item := range d.items {
		for attr, av := range item {
			if s, ok := av.(*dtypes.AttributeValueMemberS); ok && strings.Contains(s.Value, "CLEAR123") {
				t.Fatalf("cleartext leaked into DDB %s.%s: %s", k, attr, s.Value)
			}
		}
	}
}

func TestHandle_RevealPasscode_404WhenMissing(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	req := makeReq("POST", "/candidates/biz-1/website/nope/reveal-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "nope"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

func TestHandle_RevealPasscode_409WhenCipherWiped(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	// Wipe the cipher on the seeded row.
	row := d.items[keyOf("BUSINESS#biz-1", "WEBSITE#web-1")]
	delete(row, "passcodeCipher")
	passcodeOpsProvider = revealOps("SHOULD_NOT_BE_CALLED", nil)
	t.Cleanup(func() { passcodeOpsProvider = defaultPasscodeOps })

	req := makeReq("POST", "/candidates/biz-1/website/web-1/reveal-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 409 {
		t.Errorf("StatusCode = %d, want 409 (cipher wiped)", resp.StatusCode)
	}
}

func TestHandle_RevealPasscode_409WhenRevoked(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	row := d.items[keyOf("BUSINESS#biz-1", "WEBSITE#web-1")]
	row["passcodeRevokedAt"] = &dtypes.AttributeValueMemberS{Value: "2026-05-15T12:00:00Z"}
	passcodeOpsProvider = revealOps("SHOULD_NOT_BE_CALLED", nil)
	t.Cleanup(func() { passcodeOpsProvider = defaultPasscodeOps })

	req := makeReq("POST", "/candidates/biz-1/website/web-1/reveal-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 409 {
		t.Errorf("StatusCode = %d, want 409 (revoked)", resp.StatusCode)
	}
	if strings.Contains(resp.Body, "SHOULD_NOT_BE_CALLED") {
		t.Fatal("Decrypt must not be called when the passcode is revoked")
	}
}

func TestHandle_RevealPasscode_409WhenExpired(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d, "published")
	row := d.items[keyOf("BUSINESS#biz-1", "WEBSITE#web-1")]
	row["passcodeRevealableUntil"] = &dtypes.AttributeValueMemberN{Value: "1000"} // 1970
	passcodeOpsProvider = revealOps("SHOULD_NOT_BE_CALLED", nil)
	t.Cleanup(func() { passcodeOpsProvider = defaultPasscodeOps })

	req := makeReq("POST", "/candidates/biz-1/website/web-1/reveal-passcode", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 409 {
		t.Errorf("StatusCode = %d, want 409 (window expired)", resp.StatusCode)
	}
}

func TestHandle_405Default(t *testing.T) {
	setup(t)
	req := makeReq("DELETE", "/candidates/biz-1/website/web-1", "",
		map[string]string{"businessId": "biz-1", "websiteId": "web-1"})
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 405 {
		t.Errorf("StatusCode = %d, want 405", resp.StatusCode)
	}
}
