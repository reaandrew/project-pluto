package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

type fakeDDB struct {
	lastQuery *dynamodb.QueryInput
	out       *dynamodb.QueryOutput
	err       error
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
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{out: &dynamodb.QueryOutput{}}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func bizItem(id, name, gsi1sk, status string) map[string]dtypes.AttributeValue {
	return map[string]dtypes.AttributeValue{
		"id":       &dtypes.AttributeValueMemberS{Value: id},
		"name":     &dtypes.AttributeValueMemberS{Value: name},
		"domain":   &dtypes.AttributeValueMemberS{Value: id + ".co.uk"},
		"vertical": &dtypes.AttributeValueMemberS{Value: "trades"},
		"location": &dtypes.AttributeValueMemberS{Value: "Manchester"},
		"status":   &dtypes.AttributeValueMemberS{Value: status},
		"gsi1sk":   &dtypes.AttributeValueMemberS{Value: gsi1sk},
	}
}

func makeReq(method string, qs map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{QueryStringParameters: qs}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = "/queue"
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
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestHandle_NonGet_405(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), makeReq("POST", nil))
	if resp.StatusCode != 405 {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestHandle_DefaultStatus_QueriesGSI1Descending(t *testing.T) {
	f := setup(t)
	f.out = &dynamodb.QueryOutput{Items: []map[string]dtypes.AttributeValue{
		bizItem("biz-1", "Acme", "0.8600#biz-1", "awaiting_review"),
		bizItem("biz-2", "Beta", "0.4200#biz-2", "awaiting_review"),
	}}
	resp, err := handle(context.Background(), makeReq("GET", nil))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s)", resp.StatusCode, resp.Body)
	}
	if got := *f.lastQuery.IndexName; got != "gsi1" {
		t.Errorf("IndexName = %q, want gsi1", got)
	}
	if f.lastQuery.ScanIndexForward == nil || *f.lastQuery.ScanIndexForward {
		t.Error("ScanIndexForward must be false (highest priority first)")
	}
	pk := f.lastQuery.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	if pk != "BUSINESS#STATUS#awaiting_review" {
		t.Errorf("gsi1pk = %q", pk)
	}
	var got queueResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "awaiting_review" || len(got.Items) != 2 {
		t.Fatalf("response drift: %+v", got)
	}
	if got.Items[0].PriorityScore != 0.86 || got.Items[1].PriorityScore != 0.42 {
		t.Errorf("priorityScore not parsed from gsi1sk: %+v", got.Items)
	}
	if got.Items[0].ID != "biz-1" || got.Items[0].Name != "Acme" {
		t.Errorf("item mapping drift: %+v", got.Items[0])
	}
	if got.NextCursor != "" {
		t.Errorf("no LastEvaluatedKey → NextCursor must be empty, got %q", got.NextCursor)
	}
}

func TestHandle_UnsupportedStatus_400(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), makeReq("GET", map[string]string{"status": "bogus"}))
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandle_BadLimit_400(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), makeReq("GET", map[string]string{"limit": "-3"}))
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandle_LimitCappedAndPassed(t *testing.T) {
	f := setup(t)
	if _, err := handle(context.Background(), makeReq("GET", map[string]string{"limit": "500"})); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if f.lastQuery.Limit == nil || *f.lastQuery.Limit != maxLimit {
		t.Errorf("limit not capped to %d: %v", maxLimit, f.lastQuery.Limit)
	}
}

func TestHandle_CursorRoundTrip(t *testing.T) {
	f := setup(t)
	lek := map[string]dtypes.AttributeValue{
		"pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#biz-2"},
		"sk":     &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		"gsi1pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#awaiting_review"},
		"gsi1sk": &dtypes.AttributeValueMemberS{Value: "0.4200#biz-2"},
	}
	f.out = &dynamodb.QueryOutput{
		Items:            []map[string]dtypes.AttributeValue{bizItem("biz-1", "Acme", "0.86#biz-1", "awaiting_review")},
		LastEvaluatedKey: lek,
	}
	resp, _ := handle(context.Background(), makeReq("GET", nil))
	var page1 queueResponse
	_ = json.Unmarshal([]byte(resp.Body), &page1)
	if page1.NextCursor == "" {
		t.Fatal("expected a NextCursor when LastEvaluatedKey is set")
	}
	// Feed the cursor back — it must decode into ExclusiveStartKey.
	f.out = &dynamodb.QueryOutput{}
	if _, err := handle(context.Background(), makeReq("GET", map[string]string{"cursor": page1.NextCursor})); err != nil {
		t.Fatalf("handle page2: %v", err)
	}
	esk := f.lastQuery.ExclusiveStartKey
	if esk["gsi1sk"].(*dtypes.AttributeValueMemberS).Value != "0.4200#biz-2" ||
		esk["pk"].(*dtypes.AttributeValueMemberS).Value != "BUSINESS#biz-2" {
		t.Errorf("cursor did not round-trip into ExclusiveStartKey: %+v", esk)
	}
}

func TestHandle_InvalidCursor_400(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), makeReq("GET", map[string]string{"cursor": "!!!not-base64!!!"}))
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandle_QueryError_500(t *testing.T) {
	f := setup(t)
	f.err = context.DeadlineExceeded
	resp, _ := handle(context.Background(), makeReq("GET", nil))
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}
