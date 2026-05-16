package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

type fakeDDB struct {
	lastQuery *dynamodb.QueryInput
	out       *dynamodb.QueryOutput
}

func (f *fakeDDB) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
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
	f.lastQuery = in
	if f.out != nil {
		return f.out, nil
	}
	return &dynamodb.QueryOutput{}, nil
}

func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func fbItem(id, subject, action, payload string) map[string]dtypes.AttributeValue {
	m := map[string]dtypes.AttributeValue{
		"id":        &dtypes.AttributeValueMemberS{Value: id},
		"subject":   &dtypes.AttributeValueMemberS{Value: subject},
		"subjectId": &dtypes.AttributeValueMemberS{Value: "s-" + id},
		"actor":     &dtypes.AttributeValueMemberS{Value: "cog-1"},
		"action":    &dtypes.AttributeValueMemberS{Value: action},
		"vertical":  &dtypes.AttributeValueMemberS{Value: "accountants"},
		"createdAt": &dtypes.AttributeValueMemberS{Value: "2026-05-16T12:00:00Z"},
	}
	if payload != "" {
		m["originalPayload"] = &dtypes.AttributeValueMemberS{Value: payload}
		m["editedPayload"] = &dtypes.AttributeValueMemberS{Value: payload}
	}
	return m
}

func opReq(method string, qs map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{QueryStringParameters: qs}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = "/feedback"
	r.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
			Claims: map[string]string{"cognito:groups": "[operator]", "sub": "cog-test"},
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

func TestHandle_NonGet_405(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), opReq("POST", nil))
	if resp.StatusCode != 405 {
		t.Errorf("status=%d want 405", resp.StatusCode)
	}
}

func TestList_DefaultVertical_NewestFirst_NoPayloads(t *testing.T) {
	f := setup(t)
	f.out = &dynamodb.QueryOutput{Items: []map[string]dtypes.AttributeValue{
		fbItem("f-1", "email", "edit", `{"body":"Use access code {{PASSCODE}}"}`),
		fbItem("f-2", "spec", "approve", ""),
	}}
	resp, _ := handle(context.Background(), opReq("GET", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	pk := f.lastQuery.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	if pk != "FEEDBACK#default" {
		t.Errorf("default partition = %q", pk)
	}
	if f.lastQuery.ScanIndexForward == nil || *f.lastQuery.ScanIndexForward {
		t.Error("must query newest-first")
	}
	// The list must never leak the captured payload bodies.
	if strings.Contains(resp.Body, "PASSCODE") || strings.Contains(resp.Body, "originalPayload") {
		t.Fatalf("feedback list leaked payload body: %s", resp.Body)
	}
	var got feedbackResponse
	_ = json.Unmarshal([]byte(resp.Body), &got)
	if len(got.Items) != 2 || got.Items[0].ID != "f-1" || got.Items[0].Subject != "email" {
		t.Errorf("items drift: %+v", got.Items)
	}
}

func TestList_VerticalAndSubjectFilter(t *testing.T) {
	f := setup(t)
	resp, _ := handle(context.Background(), opReq("GET", map[string]string{"vertical": "dentist", "subject": "website"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if v := f.lastQuery.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value; v != "FEEDBACK#dentist" {
		t.Errorf("pk = %q", v)
	}
	if f.lastQuery.FilterExpression == nil || !strings.Contains(*f.lastQuery.FilterExpression, "subject = :subj") {
		t.Error("subject filter not applied")
	}
}

func TestList_RejectsBadSubjectAndLimit(t *testing.T) {
	setup(t)
	if r, _ := handle(context.Background(), opReq("GET", map[string]string{"subject": "bogus"})); r.StatusCode != 400 {
		t.Errorf("bad subject → %d want 400", r.StatusCode)
	}
	if r, _ := handle(context.Background(), opReq("GET", map[string]string{"limit": "0"})); r.StatusCode != 400 {
		t.Errorf("bad limit → %d want 400", r.StatusCode)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	key := map[string]dtypes.AttributeValue{
		"pk": &dtypes.AttributeValueMemberS{Value: "FEEDBACK#default"},
		"sk": &dtypes.AttributeValueMemberS{Value: "2026-05-16T12:00:00Z#abc"},
	}
	enc, err := encodeCursor(key)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	dec, err := decodeCursor(enc)
	if err != nil || dec["pk"].(*dtypes.AttributeValueMemberS).Value != "FEEDBACK#default" {
		t.Errorf("round-trip drift: %v %v", dec, err)
	}
	if _, err := decodeCursor("!!"); err == nil {
		t.Error("garbage cursor must error")
	}
}
