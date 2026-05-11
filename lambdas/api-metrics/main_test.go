package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- fakes ----------------------------------------------------------

type fakeDDB struct {
	queryRows []map[string]dtypes.AttributeValue
	queryErr  error
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
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
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &dynamodb.QueryOutput{Items: f.queryRows}, nil
}

type fakeInvoker struct {
	gotInput      *awslambda.InvokeInput
	invokeErr     error
	functionError string
	payload       string
	status        int32
}

func (f *fakeInvoker) Invoke(_ context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.gotInput = in
	if f.invokeErr != nil {
		return nil, f.invokeErr
	}
	out := &awslambda.InvokeOutput{StatusCode: f.status}
	if out.StatusCode == 0 {
		out.StatusCode = 200
	}
	if f.functionError != "" {
		s := f.functionError
		out.FunctionError = &s
	}
	out.Payload = []byte(f.payload)
	return out, nil
}

// --- setup ---------------------------------------------------------

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("ENVIRONMENT", "unit-test")
	t.Setenv("DISCOVER_FUNCTION_NAME", "ai-website-agency-discover-unit-test")
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return fake
}

func operatorReq(method, path, body string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RouteKey: method + " " + path,
		Body:     body,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: method, Path: path,
			},
			Authorizer: &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
				JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
					Claims: map[string]string{"cognito:groups": "[operator]"},
				},
			},
		},
	}
}

func businessItem(name, domain, createdAt string) map[string]dtypes.AttributeValue {
	row := businessRow{
		ID: "id-" + domain, Name: name, Domain: domain,
		Source: "csv", Status: "new", CreatedAt: createdAt,
	}
	item, _ := attributevalue.MarshalMap(row)
	return item
}

// --- auth gate -----------------------------------------------------

func TestForbiddenWithoutOperatorGroup(t *testing.T) {
	reset(t)
	req := events.APIGatewayV2HTTPRequest{
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET", Path: "/metrics/discoveries"},
		},
	}
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

// --- GET /metrics/discoveries -------------------------------------

func TestDiscoveries_HappyPath(t *testing.T) {
	fake := reset(t)
	today := time.Now().UTC().Format(time.RFC3339)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	fake.queryRows = []map[string]dtypes.AttributeValue{
		businessItem("Acme", "acme.co.uk", today),
		businessItem("Beta", "beta.co.uk", yesterday),
	}

	resp, _ := handle(context.Background(), operatorReq("GET", "/metrics/discoveries", ""))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var r discoveriesResponse
	if err := json.Unmarshal([]byte(resp.Body), &r); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.Body)
	}
	if len(r.Recent) != 2 {
		t.Fatalf("len(Recent)=%d, want 2", len(r.Recent))
	}
	if r.Recent[0].Name != "Acme" {
		t.Errorf("recent[0].Name=%q", r.Recent[0].Name)
	}
	if r.TotalLast7Day != 2 {
		t.Errorf("TotalLast7Day=%d, want 2", r.TotalLast7Day)
	}
	if len(r.CountsByDay) != 7 {
		t.Errorf("CountsByDay has %d keys, want 7", len(r.CountsByDay))
	}
}

func TestDiscoveries_DDBError_Returns500(t *testing.T) {
	fake := reset(t)
	fake.queryErr = errors.New("ddb down")
	resp, _ := handle(context.Background(), operatorReq("GET", "/metrics/discoveries", ""))
	if resp.StatusCode != 500 {
		t.Errorf("status=%d, want 500", resp.StatusCode)
	}
}

// --- countsByDay ---------------------------------------------------

func TestCountsByDay_BucketsByCreatedAt(t *testing.T) {
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	rows := []businessRow{
		{CreatedAt: "2026-05-11T10:00:00Z"},
		{CreatedAt: "2026-05-11T11:00:00Z"},
		{CreatedAt: "2026-05-10T08:00:00Z"},
		{CreatedAt: "2026-05-04T08:00:00Z"}, // 7 days ago — falls in the bucket
		{CreatedAt: "2026-05-03T08:00:00Z"}, // older than 7 — drops off
	}
	out := countsByDay(rows, now)
	if out["2026-05-11"] != 2 {
		t.Errorf("today=%d, want 2", out["2026-05-11"])
	}
	if out["2026-05-10"] != 1 {
		t.Errorf("yesterday=%d, want 1", out["2026-05-10"])
	}
	if _, ok := out["2026-05-03"]; ok {
		t.Errorf("8-days-ago shouldn't be a bucket: %v", out)
	}
}

// --- POST /metrics/discoveries/run --------------------------------

func TestRun_HappyPath_Invokes202(t *testing.T) {
	reset(t)
	inv := &fakeInvoker{status: 200, payload: "null"}
	deps := handlerDeps{
		DiscoverFunctionName: "discover-fn",
		Invoker:              inv,
		Now:                  func() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) },
	}
	resp, _ := handleRunDiscovery(context.Background(), deps)
	if resp.StatusCode != 202 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if inv.gotInput == nil {
		t.Fatal("Invoke was not called")
	}
	if *inv.gotInput.FunctionName != "discover-fn" {
		t.Errorf("FunctionName=%q", *inv.gotInput.FunctionName)
	}
	if inv.gotInput.InvocationType != lambdatypes.InvocationTypeRequestResponse {
		t.Errorf("InvocationType=%v, want RequestResponse", inv.gotInput.InvocationType)
	}
}

func TestRun_SurfacesFunctionError(t *testing.T) {
	reset(t)
	inv := &fakeInvoker{
		status:        200,
		functionError: "Unhandled",
		payload:       `{"errorMessage":"boom"}`,
	}
	deps := handlerDeps{DiscoverFunctionName: "f", Invoker: inv, Now: time.Now}
	resp, _ := handleRunDiscovery(context.Background(), deps)
	if resp.StatusCode != 502 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestRun_SurfacesInvokeError(t *testing.T) {
	reset(t)
	inv := &fakeInvoker{invokeErr: errors.New("network down")}
	deps := handlerDeps{DiscoverFunctionName: "f", Invoker: inv, Now: time.Now}
	resp, _ := handleRunDiscovery(context.Background(), deps)
	if resp.StatusCode != 502 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestRun_MissingFunctionName_Returns500(t *testing.T) {
	reset(t)
	deps := handlerDeps{DiscoverFunctionName: "", Invoker: &fakeInvoker{}, Now: time.Now}
	resp, _ := handleRunDiscovery(context.Background(), deps)
	if resp.StatusCode != 500 {
		t.Fatalf("status=%d, want 500", resp.StatusCode)
	}
}

// --- method routing -----------------------------------------------

func TestMethodNotAllowed(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("DELETE", "/metrics/discoveries", ""))
	if resp.StatusCode != 405 {
		t.Fatalf("status=%d, want 405", resp.StatusCode)
	}
}
