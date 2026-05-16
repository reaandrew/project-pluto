package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
)

type fakeDDB struct {
	query   *dynamodb.QueryOutput
	item    map[string]dtypes.AttributeValue
	updates []*dynamodb.UpdateItemInput
	puts    []*dynamodb.PutItemInput
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.puts = append(f.puts, in)
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{Item: f.item}, nil
}
func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updates = append(f.updates, in)
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if f.query != nil {
		return f.query, nil
	}
	return &dynamodb.QueryOutput{}, nil
}

type fakeEB struct{ puts int }

func (f *fakeEB) PutEvents(_ context.Context, _ *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.puts++
	return &eventbridge.PutEventsOutput{}, nil
}

func setup(t *testing.T) (*fakeDDB, *fakeEB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	d := &fakeDDB{}
	ddb.SetClient(d)
	eb := &fakeEB{}
	newPublisher = func(context.Context) (*pkgevents.Publisher, error) {
		return pkgevents.NewPublisherWithClient(eb, "test-bus"), nil
	}
	t.Cleanup(func() {
		ddb.SetClient(nil)
		newPublisher = pkgevents.NewPublisher
	})
	return d, eb
}

func deltaItem(id, kind, vertical, status string) map[string]dtypes.AttributeValue {
	return map[string]dtypes.AttributeValue{
		"pk":              &dtypes.AttributeValueMemberS{Value: "DELTA#" + kind + "#" + vertical},
		"sk":              &dtypes.AttributeValueMemberS{Value: id},
		"type":            &dtypes.AttributeValueMemberS{Value: "TunerDelta"},
		"id":              &dtypes.AttributeValueMemberS{Value: id},
		"kind":            &dtypes.AttributeValueMemberS{Value: kind},
		"vertical":        &dtypes.AttributeValueMemberS{Value: vertical},
		"status":          &dtypes.AttributeValueMemberS{Value: status},
		"proposedPayload": &dtypes.AttributeValueMemberS{Value: `{"addIncludeKeywords":["chartered"],"rationale":"r"}`},
		"rationale":       &dtypes.AttributeValueMemberS{Value: "r"},
		"promptId":        &dtypes.AttributeValueMemberS{Value: "tuner.targeting.v1"},
		"createdAt":       &dtypes.AttributeValueMemberS{Value: "2026-05-16T02:00:00Z"},
	}
}

func opReq(method, path, body string, qs map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{QueryStringParameters: qs, Body: body}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = path
	r.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
			Claims: map[string]string{"cognito:groups": "[operator]", "sub": "cog-1"},
		},
	}
	return r
}

func TestHandle_NotOperator_403(t *testing.T) {
	setup(t)
	r := events.APIGatewayV2HTTPRequest{}
	r.RequestContext.HTTP.Method = "GET"
	resp, _ := handle(context.Background(), r)
	if resp.StatusCode != 403 {
		t.Errorf("status=%d want 403", resp.StatusCode)
	}
}

func TestList_DefaultsToPending(t *testing.T) {
	d, _ := setup(t)
	d.query = &dynamodb.QueryOutput{Items: []map[string]dtypes.AttributeValue{
		deltaItem("w1", "style", "default", "pending"),
	}}
	resp, _ := handle(context.Background(), opReq("GET", "/tuners", "", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got tunersResponse
	_ = json.Unmarshal([]byte(resp.Body), &got)
	if got.Status != "pending" || len(got.Items) != 1 || got.Items[0].Kind != "style" || got.Items[0].Ref == "" {
		t.Errorf("list drift: %+v", got)
	}
	if string(got.Items[0].Proposed) == "" {
		t.Error("proposed payload must be surfaced for the diff view")
	}
}

func TestList_BadStatus_400(t *testing.T) {
	setup(t)
	if r, _ := handle(context.Background(), opReq("GET", "/tuners", "", map[string]string{"status": "weird"})); r.StatusCode != 400 {
		t.Errorf("status=%d want 400", r.StatusCode)
	}
}

func TestApply_TargetingAdvisory_RecordsAndEvents(t *testing.T) {
	d, eb := setup(t)
	d.item = deltaItem("w1", "targeting", "accountants", "pending")
	ref := encodeRef("DELTA#targeting#accountants", "w1")
	body, _ := json.Marshal(decisionBody{Ref: ref})
	resp, _ := handle(context.Background(), opReq("POST", "/tuners/w1/apply", string(body), nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	// status → applied
	if len(d.updates) != 1 {
		t.Fatalf("want 1 status UpdateItem, got %d", len(d.updates))
	}
	if v := d.updates[0].ExpressionAttributeValues[":g"].(*dtypes.AttributeValueMemberS).Value; v != "DELTA#STATUS#applied" {
		t.Errorf("delta must leave pending: gsi1pk=%q", v)
	}
	// a Feedback row was written (PutItem) and profile.updated emitted.
	if len(d.puts) == 0 {
		t.Error("expected a Feedback audit row PutItem")
	}
	// 2 events: feedback.captured (from feedback.Capture) + profile.updated.
	if eb.puts != 2 {
		t.Errorf("apply must emit feedback.captured + profile.updated, got %d events", eb.puts)
	}
}

func TestDismiss_RecordsNoEvent(t *testing.T) {
	d, eb := setup(t)
	d.item = deltaItem("w2", "style", "default", "pending")
	ref := encodeRef("DELTA#style#default", "w2")
	body, _ := json.Marshal(decisionBody{Ref: ref, Reason: "noise"})
	resp, _ := handle(context.Background(), opReq("POST", "/tuners/w2/dismiss", string(body), nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if v := d.updates[0].ExpressionAttributeValues[":g"].(*dtypes.AttributeValueMemberS).Value; v != "DELTA#STATUS#dismissed" {
		t.Errorf("gsi1pk=%q want dismissed", v)
	}
	// dismiss emits only feedback.captured — never profile.updated.
	if eb.puts != 1 {
		t.Errorf("dismiss must emit exactly the feedback.captured event, got %d", eb.puts)
	}
}

func TestDecision_NotPending_409(t *testing.T) {
	d, _ := setup(t)
	d.item = deltaItem("w3", "style", "default", "applied")
	ref := encodeRef("DELTA#style#default", "w3")
	body, _ := json.Marshal(decisionBody{Ref: ref})
	resp, _ := handle(context.Background(), opReq("POST", "/tuners/w3/dismiss", string(body), nil))
	if resp.StatusCode != 409 {
		t.Errorf("status=%d want 409 (already decided)", resp.StatusCode)
	}
}

func TestDecision_BadInputs(t *testing.T) {
	setup(t)
	if r, _ := handle(context.Background(), opReq("POST", "/tuners/x/apply", "{", nil)); r.StatusCode != 400 {
		t.Errorf("bad json → %d want 400", r.StatusCode)
	}
	if r, _ := handle(context.Background(), opReq("POST", "/tuners/x/apply", `{"ref":"!!"}`, nil)); r.StatusCode != 400 {
		t.Errorf("bad ref → %d want 400", r.StatusCode)
	}
}

func TestMergeList(t *testing.T) {
	got := mergeList([]string{"a", "b"}, []string{"b", "c", " "}, []string{"a"})
	if strings.Join(got, ",") != "b,c" {
		t.Errorf("mergeList drift: %v", got)
	}
}

func TestRefRoundTrip(t *testing.T) {
	pk, sk, err := decodeRef(encodeRef("DELTA#style#default", "w1"))
	if err != nil || pk != "DELTA#style#default" || sk != "w1" {
		t.Errorf("ref drift: %q %q %v", pk, sk, err)
	}
}
