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
	updates   []*dynamodb.UpdateItemInput
}

func (f *fakeDDB) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
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

func triageItem(pk, sk, id, biz, cat string) map[string]dtypes.AttributeValue {
	return map[string]dtypes.AttributeValue{
		"pk":          &dtypes.AttributeValueMemberS{Value: pk},
		"sk":          &dtypes.AttributeValueMemberS{Value: sk},
		"id":          &dtypes.AttributeValueMemberS{Value: id},
		"businessId":  &dtypes.AttributeValueMemberS{Value: biz},
		"category":    &dtypes.AttributeValueMemberS{Value: cat},
		"confidence":  &dtypes.AttributeValueMemberN{Value: "0.42"},
		"rationale":   &dtypes.AttributeValueMemberS{Value: "ambiguous"},
		"bodyExcerpt": &dtypes.AttributeValueMemberS{Value: "who is this?"},
		"triageState": &dtypes.AttributeValueMemberS{Value: "operator_inbox"},
		"createdAt":   &dtypes.AttributeValueMemberS{Value: "2026-05-16T12:00:00Z"},
	}
}

func opReq(method, path, body string, qs map[string]string, pp map[string]string) events.APIGatewayV2HTTPRequest {
	r := events.APIGatewayV2HTTPRequest{QueryStringParameters: qs, PathParameters: pp, Body: body}
	r.RequestContext.HTTP.Method = method
	r.RequestContext.HTTP.Path = path
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

func TestHandle_BadMethod_405(t *testing.T) {
	setup(t)
	resp, _ := handle(context.Background(), opReq("DELETE", "/replies", "", nil, nil))
	if resp.StatusCode != 405 {
		t.Errorf("status=%d want 405", resp.StatusCode)
	}
}

func TestList_DefaultsToOperatorInbox_NewestFirst(t *testing.T) {
	f := setup(t)
	f.out = &dynamodb.QueryOutput{Items: []map[string]dtypes.AttributeValue{
		triageItem("BUSINESS#biz-1", "REPLY_TRIAGE#t-1", "t-1", "biz-1", "unknown"),
		triageItem("REPLYTRIAGE#INBOX", "ITEM#2026-05-16T11:00:00Z#t-2", "t-2", "", "unknown"),
	}}
	resp, _ := handle(context.Background(), opReq("GET", "/replies", "", nil, nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	pk := f.lastQuery.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	if pk != "REPLYTRIAGE#STATUS#operator_inbox" {
		t.Errorf("default partition = %q", pk)
	}
	if f.lastQuery.ScanIndexForward == nil || *f.lastQuery.ScanIndexForward {
		t.Error("must query newest-first (ScanIndexForward=false)")
	}
	var got repliesResponse
	_ = json.Unmarshal([]byte(resp.Body), &got)
	if len(got.Items) != 2 || got.Items[0].ID != "t-1" || got.Items[0].Ref == "" {
		t.Errorf("items drift: %+v", got.Items)
	}
	// ref must round-trip to the real key.
	pk2, sk2, err := decodeRef(got.Items[0].Ref)
	if err != nil || pk2 != "BUSINESS#biz-1" || sk2 != "REPLY_TRIAGE#t-1" {
		t.Errorf("ref decode drift: %q %q %v", pk2, sk2, err)
	}
}

func TestList_CategoryFilter(t *testing.T) {
	f := setup(t)
	f.out = &dynamodb.QueryOutput{}
	resp, _ := handle(context.Background(), opReq("GET", "/replies", "", map[string]string{"category": "unsubscribe"}, nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if f.lastQuery.FilterExpression == nil || !strings.Contains(*f.lastQuery.FilterExpression, "category = :cat") {
		t.Error("category filter not applied")
	}
	if v := f.lastQuery.ExpressionAttributeValues[":cat"].(*dtypes.AttributeValueMemberS).Value; v != "unsubscribe" {
		t.Errorf(":cat = %q", v)
	}
}

func TestList_RejectsBadStatusAndCategory(t *testing.T) {
	setup(t)
	if r, _ := handle(context.Background(), opReq("GET", "/replies", "", map[string]string{"status": "bogus"}, nil)); r.StatusCode != 400 {
		t.Errorf("bad status → %d want 400", r.StatusCode)
	}
	if r, _ := handle(context.Background(), opReq("GET", "/replies", "", map[string]string{"category": "spam"}, nil)); r.StatusCode != 400 {
		t.Errorf("bad category → %d want 400", r.StatusCode)
	}
}

func TestReclassify_AttributedUnsubscribe_UpdatesItemAndBusiness(t *testing.T) {
	f := setup(t)
	ref := encodeRef("BUSINESS#biz-1", "REPLY_TRIAGE#t-1")
	body, _ := json.Marshal(reclassifyBody{Ref: ref, NewCategory: "unsubscribe", Notes: "clearly opting out"})
	resp, _ := handle(context.Background(), opReq("POST", "/replies/t-1/reclassify", string(body), nil, map[string]string{"id": "t-1"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if len(f.updates) != 2 {
		t.Fatalf("want 2 UpdateItems (triage + business), got %d", len(f.updates))
	}
	// 1st: the ReplyTriage row → reviewed.
	u0 := f.updates[0]
	if u0.Key["pk"].(*dtypes.AttributeValueMemberS).Value != "BUSINESS#biz-1" ||
		u0.Key["sk"].(*dtypes.AttributeValueMemberS).Value != "REPLY_TRIAGE#t-1" {
		t.Errorf("triage update keyed wrong: %+v", u0.Key)
	}
	if v := u0.ExpressionAttributeValues[":gpk"].(*dtypes.AttributeValueMemberS).Value; v != "REPLYTRIAGE#STATUS#reviewed" {
		t.Errorf("triage must leave the inbox (gsi1pk=%q)", v)
	}
	// 2nd: the Business row → rejected_after_review.
	u1 := f.updates[1]
	if u1.Key["sk"].(*dtypes.AttributeValueMemberS).Value != "PROFILE" ||
		u1.ExpressionAttributeValues[":s"].(*dtypes.AttributeValueMemberS).Value != "rejected_after_review" {
		t.Errorf("business side-effect wrong: %+v", u1.ExpressionAttributeValues)
	}
}

func TestReclassify_Unattributed_NoBusinessWrite(t *testing.T) {
	f := setup(t)
	ref := encodeRef("REPLYTRIAGE#INBOX", "ITEM#2026-05-16T11:00:00Z#t-2")
	body, _ := json.Marshal(reclassifyBody{Ref: ref, NewCategory: "positive_interest"})
	resp, _ := handle(context.Background(), opReq("POST", "/replies/t-2/reclassify", string(body), nil, map[string]string{"id": "t-2"}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if len(f.updates) != 1 {
		t.Errorf("unattributed must only update the triage row, got %d updates", len(f.updates))
	}
}

func TestReclassify_BadInputs(t *testing.T) {
	setup(t)
	good := encodeRef("REPLYTRIAGE#INBOX", "ITEM#x#t-9")
	cases := []struct {
		name, body string
		want       int
	}{
		{"bad json", "{", 400},
		{"bad category", `{"ref":"` + good + `","newCategory":"spam"}`, 400},
		{"bad ref", `{"ref":"!!!","newCategory":"unknown"}`, 400},
	}
	for _, c := range cases {
		r, _ := handle(context.Background(), opReq("POST", "/replies/t-9/reclassify", c.body, nil, map[string]string{"id": "t-9"}))
		if r.StatusCode != c.want {
			t.Errorf("%s: status=%d want %d", c.name, r.StatusCode, c.want)
		}
	}
}

func TestRefRoundTrip(t *testing.T) {
	pk, sk, err := decodeRef(encodeRef("BUSINESS#b", "REPLY_TRIAGE#i"))
	if err != nil || pk != "BUSINESS#b" || sk != "REPLY_TRIAGE#i" {
		t.Errorf("round-trip drift: %q %q %v", pk, sk, err)
	}
	if _, _, err := decodeRef("not-base64!!"); err == nil {
		t.Error("garbage ref must error")
	}
}
