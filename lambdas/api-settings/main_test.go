package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
)

// --- fakes ---------------------------------------------------------------

type fakeDDB struct {
	getOut    *dynamodb.GetItemOutput
	getErr    error
	putErr    error
	putInputs []*dynamodb.PutItemInput
}

func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putInputs = append(f.putInputs, in)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
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

func itemFor(t *testing.T, s killswitch.Settings) *dynamodb.GetItemOutput {
	t.Helper()
	item, err := attributevalue.MarshalMap(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsPK}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsSK}
	return &dynamodb.GetItemOutput{Item: item}
}

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("ENVIRONMENT", "unit-test")
	killswitch.SetSettings(nil)
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() {
		ddb.SetClient(nil)
		killswitch.SetSettings(nil)
	})
	return fake
}

// operatorReq builds an APIGatewayV2 request signed-in as an operator.
func operatorReq(method, body string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RouteKey: method + " /settings",
		Body:     body,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: method},
			Authorizer: &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
				JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
					Claims: map[string]string{"cognito:groups": "[operator]"},
				},
			},
		},
	}
}

// --- auth gate -----------------------------------------------------------

func TestForbiddenWithoutOperatorGroup(t *testing.T) {
	reset(t)
	req := events.APIGatewayV2HTTPRequest{
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"},
		},
	}
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestForbiddenForNonOperatorGroup(t *testing.T) {
	reset(t)
	req := operatorReq("GET", "")
	req.RequestContext.Authorizer.JWT.Claims["cognito:groups"] = "[reviewer]"
	resp, err := handle(context.Background(), req)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

// --- GET -----------------------------------------------------------------

func TestGetReturnsCurrentSettings(t *testing.T) {
	fake := reset(t)
	want := killswitch.Defaults()
	fake.getOut = itemFor(t, want)

	resp, err := handle(context.Background(), operatorReq("GET", ""))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got killswitch.Settings
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.Body)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestGetReturns500WhenDDBFails(t *testing.T) {
	fake := reset(t)
	fake.getErr = errors.New("ddb down")

	resp, _ := handle(context.Background(), operatorReq("GET", ""))
	if resp.StatusCode != 500 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

// --- PATCH ---------------------------------------------------------------

func TestPatchTopLevelMergeAndPersist(t *testing.T) {
	fake := reset(t)
	current := killswitch.Defaults()
	fake.getOut = itemFor(t, current)

	resp, err := handle(context.Background(), operatorReq("PATCH", `{"pipelineEnabled": false}`))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}

	if len(fake.putInputs) != 1 {
		t.Fatalf("expected exactly 1 PutItem call, got %d", len(fake.putInputs))
	}
	put := fake.putInputs[0]

	// Round-trip the persisted item to verify the merge semantics.
	var persisted killswitch.Settings
	if err := attributevalue.UnmarshalMap(put.Item, &persisted); err != nil {
		t.Fatalf("unmarshal persisted: %v", err)
	}
	if persisted.PipelineEnabled {
		t.Errorf("persisted.PipelineEnabled = true, want false")
	}
	if persisted.Caps != current.Caps {
		t.Errorf("caps were rewritten on a top-level patch: got %+v want %+v", persisted.Caps, current.Caps)
	}
	if persisted.Stages != current.Stages {
		t.Errorf("stages were rewritten on a top-level patch: got %+v want %+v", persisted.Stages, current.Stages)
	}

	// pk/sk must always land on the row.
	if pk, _ := put.Item["pk"].(*dtypes.AttributeValueMemberS); pk == nil || pk.Value != killswitch.SettingsPK {
		t.Errorf("pk attribute missing/wrong: %+v", put.Item["pk"])
	}
	if sk, _ := put.Item["sk"].(*dtypes.AttributeValueMemberS); sk == nil || sk.Value != killswitch.SettingsSK {
		t.Errorf("sk attribute missing/wrong: %+v", put.Item["sk"])
	}

	// Response body should reflect the merged row.
	var body killswitch.Settings
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.PipelineEnabled {
		t.Errorf("response.pipelineEnabled = true, want false")
	}
}

func TestPatchSubObjectMergesFieldByField(t *testing.T) {
	fake := reset(t)
	current := killswitch.Defaults()
	fake.getOut = itemFor(t, current)

	// Sending only one cap field keeps the others — Go's json.Unmarshal
	// into a populated struct overwrites only fields that appear in the JSON.
	body := `{"caps":{"maxAuditsPerDay": 99}}`
	resp, err := handle(context.Background(), operatorReq("PATCH", body))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}

	var persisted killswitch.Settings
	if err := attributevalue.UnmarshalMap(fake.putInputs[0].Item, &persisted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if persisted.Caps.MaxAuditsPerDay != 99 {
		t.Errorf("MaxAuditsPerDay = %d, want 99", persisted.Caps.MaxAuditsPerDay)
	}
	if persisted.Caps.MaxDiscoveriesPerDay != current.Caps.MaxDiscoveriesPerDay {
		t.Errorf("MaxDiscoveriesPerDay = %d, want %d (untouched siblings retained)",
			persisted.Caps.MaxDiscoveriesPerDay, current.Caps.MaxDiscoveriesPerDay)
	}
}

func TestPatchExplicitZeroIsHonoured(t *testing.T) {
	fake := reset(t)
	fake.getOut = itemFor(t, killswitch.Defaults())

	resp, err := handle(context.Background(), operatorReq("PATCH", `{"caps":{"maxEmailsPerDay": 0}}`))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var persisted killswitch.Settings
	if err := attributevalue.UnmarshalMap(fake.putInputs[0].Item, &persisted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if persisted.Caps.MaxEmailsPerDay != 0 {
		t.Errorf("MaxEmailsPerDay = %d, want 0", persisted.Caps.MaxEmailsPerDay)
	}
}

func TestPatchRejectsUnknownTopLevelKey(t *testing.T) {
	fake := reset(t)
	fake.getOut = itemFor(t, killswitch.Defaults())

	resp, _ := handle(context.Background(), operatorReq("PATCH", `{"surprise": true}`))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if len(fake.putInputs) != 0 {
		t.Fatalf("PutItem called on rejected patch")
	}
}

func TestPatchRejectsInvalidJSON(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("PATCH", `{not json`))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestPatchRejectsEmptyBody(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("PATCH", ""))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestPatchRejectsEmptyObject(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("PATCH", `{}`))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestPatchPropagatesToCacheImmediately(t *testing.T) {
	fake := reset(t)
	fake.getOut = itemFor(t, killswitch.Defaults())

	if _, err := handle(context.Background(), operatorReq("PATCH", `{"pipelineEnabled": false}`)); err != nil {
		t.Fatalf("patch: %v", err)
	}

	// Subsequent killswitch.Get must hit the warm cache and reflect the
	// new value — not bounce back through DDB.
	cached, err := killswitch.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cached.PipelineEnabled {
		t.Fatalf("post-PATCH cache still says pipelineEnabled=true")
	}
}

// --- method routing ------------------------------------------------------

func TestMethodNotAllowed(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("DELETE", ""))
	if resp.StatusCode != 405 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}
