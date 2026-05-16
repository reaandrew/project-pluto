package main

import (
	"context"
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

const realCode = "H7Q32KX9" // the cleartext that must NEVER reach feedback

type fakeDDB struct {
	items map[string]map[string]dtypes.AttributeValue
}

func newFakeDDB() *fakeDDB       { return &fakeDDB{items: map[string]map[string]dtypes.AttributeValue{}} }
func keyOf(pk, sk string) string { return pk + "|" + sk }

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
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
func (f *fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pkAttr := in.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	prefix := in.ExpressionAttributeValues[":prefix"].(*dtypes.AttributeValueMemberS).Value
	var hits []map[string]dtypes.AttributeValue
	for k, item := range f.items {
		parts := strings.SplitN(k, "|", 2)
		if parts[0] == pkAttr && strings.HasPrefix(parts[1], prefix) {
			hits = append(hits, item)
		}
	}
	return &dynamodb.QueryOutput{Items: hits}, nil
}

type fakeEB struct{ puts []*eventbridge.PutEventsInput }

func (f *fakeEB) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.puts = append(f.puts, in)
	return &eventbridge.PutEventsOutput{}, nil
}

func setup(t *testing.T) (*fakeDDB, *fakeEB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("EVENT_BUS_NAME", "test-bus")
	d := newFakeDDB()
	ddb.SetClient(d)
	eb := &fakeEB{}
	cachedPublisher = pkgevents.NewPublisherWithClient(eb, "test-bus")
	feedback.SetNowFunc(func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) })
	feedback.SetIDFunc(func() string { return "fb-1" })
	nowFunc = func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) }
	randomHexFn = func(int) string { return "etag-test" }
	decryptProvider = func(context.Context) (func(context.Context, string) (string, error), error) {
		return func(context.Context, string) (string, error) { return realCode, nil }, nil
	}
	t.Cleanup(func() {
		ddb.SetClient(nil)
		cachedPublisher = nil
		feedback.SetNowFunc(func() time.Time { return time.Now().UTC() })
		feedback.SetIDFunc(func() string { return defaultRandomHex(16) })
		nowFunc = func() time.Time { return time.Now().UTC() }
		randomHexFn = defaultRandomHex
		decryptProvider = defaultDecryptProvider
	})
	return d, eb
}

func seedBusiness(d *fakeDDB) {
	d.items[keyOf("BUSINESS#biz-1", "PROFILE")] = map[string]dtypes.AttributeValue{
		"pk":       &dtypes.AttributeValueMemberS{Value: "BUSINESS#biz-1"},
		"sk":       &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		"id":       &dtypes.AttributeValueMemberS{Value: "biz-1"},
		"name":     &dtypes.AttributeValueMemberS{Value: "Acme"},
		"vertical": &dtypes.AttributeValueMemberS{Value: "accountants"},
	}
}

func seedWebsite(d *fakeDDB) {
	row := WebsiteRow{ID: "web-1", PasscodeCipher: "cipher-blob"}
	item, _ := attributevalue.MarshalMap(row)
	item["pk"] = &dtypes.AttributeValueMemberS{Value: "BUSINESS#biz-1"}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: "WEBSITE#web-1"}
	d.items[keyOf("BUSINESS#biz-1", "WEBSITE#web-1")] = item
}

func seedDraft(d *fakeDDB, status string) {
	row := EmailDraftRow{
		PK: "BUSINESS#biz-1", SK: "EMAIL_DRAFT#draft-1", Type: "EmailDraft", ID: "draft-1",
		WebsiteID: "web-1", ContactID: "con-1",
		Subject:    "Quick redesign preview for Acme",
		Body:       "Hi Jane,\nPreview: https://p/sites/web-1\nUse access code " + realCode + ".\nReply 'no thanks'.",
		OptOutLine: "Reply 'no thanks'.", WordCount: 60,
		ModelID: "anthropic.claude-haiku-4-5", PromptID: "email.v1", Status: status,
		CreatedAt: "2026-05-16T11:00:00Z", UpdatedAt: "2026-05-16T11:00:00Z", Etag: "seed",
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
			Claims: map[string]string{"cognito:groups": "[operator]", "sub": "cog-test"},
		},
	}
	return r
}

func feedbackOriginal(t *testing.T, d *fakeDDB) string {
	t.Helper()
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			if op, ok := item["originalPayload"].(*dtypes.AttributeValueMemberS); ok {
				return op.Value
			}
		}
	}
	return ""
}

func TestHandle_NotOperator_403(t *testing.T) {
	setup(t)
	r := events.APIGatewayV2HTTPRequest{PathParameters: map[string]string{"businessId": "biz-1"}}
	r.RequestContext.HTTP.Method = "GET"
	resp, _ := handle(context.Background(), r)
	if resp.StatusCode != 403 {
		t.Errorf("status=%d want 403", resp.StatusCode)
	}
}

func TestHandle_Get_ReturnsRealBodyForOperator(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedDraft(d, "draft")
	resp, _ := handle(context.Background(), makeReq("GET", "/candidates/biz-1/email", "", map[string]string{"businessId": "biz-1"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	// The operator IS authorised to see the real code on the review page.
	if !strings.Contains(resp.Body, realCode) {
		t.Errorf("GET should return the real draft body to the operator")
	}
}

func TestHandle_Approve_RedactsPasscodeInFeedback(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d)
	seedDraft(d, "draft")
	resp, err := handle(context.Background(), makeReq("POST", "/candidates/biz-1/email/draft-1/approve", `{"notes":"good"}`,
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	// email.approved published.
	var sawApproved bool
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if *e.DetailType == "email.approved" {
				sawApproved = true
			}
			if strings.Contains(*e.Detail, realCode) {
				t.Fatalf("cleartext leaked into an event: %s", *e.Detail)
			}
		}
	}
	if !sawApproved {
		t.Error("email.approved not published")
	}
	// Feedback originalPayload must be redacted.
	orig := feedbackOriginal(t, d)
	if orig == "" {
		t.Fatal("no feedback row written")
	}
	if strings.Contains(orig, realCode) {
		t.Fatalf("cleartext leaked into feedback originalPayload: %s", orig)
	}
	if !strings.Contains(orig, "{{PASSCODE}}") {
		t.Errorf("feedback originalPayload should carry the {{PASSCODE}} placeholder: %s", orig)
	}
	// approve/reject set no EditedPayload — assert it never appears.
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			if _, ok := item["editedPayload"]; ok {
				t.Error("approve must not write an editedPayload")
			}
		}
	}
}

func TestHandle_Reject_RedactsPasscodeInFeedback(t *testing.T) {
	d, eb := setup(t)
	seedBusiness(d)
	seedWebsite(d)
	seedDraft(d, "draft")
	resp, err := handle(context.Background(), makeReq("POST", "/candidates/biz-1/email/draft-1/reject", `{"notes":"off-tone"}`,
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	var sawRejected bool
	for _, p := range eb.puts {
		for _, e := range p.Entries {
			if *e.DetailType == "email.rejected" {
				sawRejected = true
			}
			if strings.Contains(*e.Detail, realCode) {
				t.Fatalf("cleartext leaked into an event: %s", *e.Detail)
			}
		}
	}
	if !sawRejected {
		t.Error("email.rejected not published")
	}
	orig := feedbackOriginal(t, d)
	if orig == "" {
		t.Fatal("no feedback row written")
	}
	if strings.Contains(orig, realCode) {
		t.Fatalf("cleartext leaked into feedback originalPayload: %s", orig)
	}
	if !strings.Contains(orig, "{{PASSCODE}}") {
		t.Errorf("feedback originalPayload should carry the {{PASSCODE}} placeholder: %s", orig)
	}
}

func TestHandle_Reject_BlanksBodyWhenCipherUnavailable(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedDraft(d, "draft") // NO website → cipher unrecoverable
	resp, _ := handle(context.Background(), makeReq("POST", "/candidates/biz-1/email/draft-1/reject", "",
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	orig := feedbackOriginal(t, d)
	if strings.Contains(orig, realCode) {
		t.Fatalf("cleartext leaked when cipher unavailable: %s", orig)
	}
	if !strings.Contains(orig, "redacted — passcode unrecoverable") {
		t.Errorf("expected fail-safe blanked body, got: %s", orig)
	}
}

func TestHandle_Edit_RedactsBothPayloads(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedWebsite(d)
	seedDraft(d, "draft")
	// Operator edits but keeps the code token in the new body too.
	newBody := `{"subject":"Tweaked subject","body":"Hi Jane,\nNew copy. Code ` + realCode + `\nReply 'no thanks'.","notes":"tightened"}`
	resp, _ := handle(context.Background(), makeReq("PATCH", "/candidates/biz-1/email/draft-1", newBody,
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	for k, item := range d.items {
		if strings.HasPrefix(k, "FEEDBACK#") {
			for _, attr := range []string{"originalPayload", "editedPayload"} {
				if s, ok := item[attr].(*dtypes.AttributeValueMemberS); ok {
					if strings.Contains(s.Value, realCode) {
						t.Fatalf("cleartext leaked into feedback %s: %s", attr, s.Value)
					}
					if !strings.Contains(s.Value, "{{PASSCODE}}") {
						t.Errorf("feedback %s not redacted to placeholder: %s", attr, s.Value)
					}
				}
			}
		}
	}
}

func TestHandle_Decision_409WhenNotDraft(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedDraft(d, "approved")
	resp, _ := handle(context.Background(), makeReq("POST", "/candidates/biz-1/email/draft-1/reject", "",
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if resp.StatusCode != 409 {
		t.Errorf("status=%d want 409", resp.StatusCode)
	}
}

func TestHandle_Approve_BlanksBodyWhenCipherUnavailable(t *testing.T) {
	d, _ := setup(t)
	seedBusiness(d)
	seedDraft(d, "draft") // NO website seeded → no cipher → code unrecoverable
	resp, _ := handle(context.Background(), makeReq("POST", "/candidates/biz-1/email/draft-1/approve", "",
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (%s)", resp.StatusCode, resp.Body)
	}
	orig := feedbackOriginal(t, d)
	if strings.Contains(orig, realCode) {
		t.Fatalf("cleartext leaked when cipher unavailable: %s", orig)
	}
	if !strings.Contains(orig, "redacted — passcode unrecoverable") {
		t.Errorf("expected fail-safe blanked body, got: %s", orig)
	}
}

func TestHandle_405Default(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), makeReq("DELETE", "/candidates/biz-1/email/draft-1", "",
		map[string]string{"businessId": "biz-1", "emailId": "draft-1"}))
	if resp.StatusCode != 405 {
		t.Errorf("status=%d want 405", resp.StatusCode)
	}
}
