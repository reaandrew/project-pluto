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

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

// --- fake DDB (mirrors the one in targeting_test.go) -----------------

type fakeDDB struct {
	getItem   map[string]map[string]dtypes.AttributeValue
	scanItems []map[string]dtypes.AttributeValue
	putErr    error
	ccfeOnPut bool
	delErr    error
	delAbsent bool
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.ccfeOnPut {
		return nil, &dtypes.ConditionalCheckFailedException{}
	}
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk, _ := in.Key["pk"].(*dtypes.AttributeValueMemberS)
	if pk == nil {
		return &dynamodb.GetItemOutput{}, nil
	}
	if item, ok := f.getItem[pk.Value]; ok {
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{Items: f.scanItems}, nil
}

func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	if f.delErr != nil {
		return nil, f.delErr
	}
	if f.delAbsent {
		return &dynamodb.DeleteItemOutput{}, nil
	}
	return &dynamodb.DeleteItemOutput{
		Attributes: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "x"},
		},
	}, nil
}

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("ENVIRONMENT", "unit-test")
	fake := &fakeDDB{getItem: map[string]map[string]dtypes.AttributeValue{}}
	ddb.SetClient(fake)
	targeting.SetNowFunc(func() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) })
	idSeq, etagSeq := 0, 0
	targeting.SetIDFunc(func(int) string { idSeq++; return "id-" + intStr(idSeq) })
	targeting.SetEtagFunc(func(int) string { etagSeq++; return "etag-" + intStr(etagSeq) })
	t.Cleanup(func() {
		ddb.SetClient(nil)
	})
	return fake
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

func operatorReq(method, path, body string, pathParams map[string]string, headers map[string]string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RouteKey:       method + " " + path,
		Body:           body,
		PathParameters: pathParams,
		Headers:        headers,
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

func validBody() string {
	p := targeting.Profile{
		Vertical: "accountants",
		Location: "Manchester, UK",
		Weights: targeting.Weights{
			WebsiteAge: 0.2, AuditScore: 0.3, BusinessSize: 0.2,
			ContactConfidence: 0.2, VerticalFit: 0.1,
		},
		Enabled: true,
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// --- auth gate -----------------------------------------------------------

func TestForbiddenWithoutOperatorGroup(t *testing.T) {
	reset(t)
	req := events.APIGatewayV2HTTPRequest{
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"},
		},
	}
	resp, _ := handle(context.Background(), req)
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

// --- POST /targeting -----------------------------------------------------

func TestCreate_HappyPath_Returns201(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("POST", "/targeting", validBody(), nil, nil))
	if resp.StatusCode != 201 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var p targeting.Profile
	if err := json.Unmarshal([]byte(resp.Body), &p); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.Body)
	}
	if p.ID == "" {
		t.Error("expected server-generated id, got empty")
	}
	if p.Etag == "" {
		t.Error("expected server-generated etag, got empty")
	}
}

func TestCreate_RejectsInvalidJSON(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("POST", "/targeting", "{not json", nil, nil))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestCreate_RejectsInvalidProfile(t *testing.T) {
	reset(t)
	bad := `{"vertical":"","location":"x","weights":{"websiteAge":0.2,"auditScore":0.2,"businessSize":0.2,"contactConfidence":0.2,"verticalFit":0.2}}`
	resp, _ := handle(context.Background(), operatorReq("POST", "/targeting", bad, nil, nil))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if !strings.Contains(resp.Body, "vertical is required") {
		t.Errorf("missing-vertical reason not surfaced: %s", resp.Body)
	}
}

// --- GET /targeting and /targeting/{id} ----------------------------------

func TestList_ReturnsEnvelope(t *testing.T) {
	fake := reset(t)
	item, _ := attributevalue.MarshalMap(targeting.Profile{Vertical: "accountants"})
	item["pk"] = &dtypes.AttributeValueMemberS{Value: "TARGET#id-1"}
	item["type"] = &dtypes.AttributeValueMemberS{Value: "TargetingProfile"}
	fake.scanItems = []map[string]dtypes.AttributeValue{item}

	resp, _ := handle(context.Background(), operatorReq("GET", "/targeting", "", nil, nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var env struct {
		Profiles []targeting.Profile `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.Body)
	}
	if len(env.Profiles) != 1 || env.Profiles[0].Vertical != "accountants" {
		t.Fatalf("unexpected list: %+v", env)
	}
}

func TestGetOne_ReturnsNotFoundForMissingID(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("GET", "/targeting/{id}", "", map[string]string{"id": "missing"}, nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

// --- PATCH /targeting/{id} ----------------------------------------------

func TestUpdate_RequiresIfMatchHeader(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("PATCH", "/targeting/{id}", validBody(), map[string]string{"id": "id-1"}, nil))
	if resp.StatusCode != 428 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestUpdate_HappyPath_RotatesEtag(t *testing.T) {
	fake := reset(t)
	// Seed an existing profile.
	existing := targeting.Profile{
		ID: "id-1", Vertical: "accountants", Location: "Manchester, UK", Etag: "etag-old",
		CreatedAt: "2026-01-01T00:00:00Z", Stats: targeting.Stats{Discovered7d: 5},
		Weights: targeting.Weights{
			WebsiteAge: 0.2, AuditScore: 0.3, BusinessSize: 0.2,
			ContactConfidence: 0.2, VerticalFit: 0.1,
		},
	}
	item, _ := attributevalue.MarshalMap(existing)
	item["pk"] = &dtypes.AttributeValueMemberS{Value: "TARGET#id-1"}
	item["type"] = &dtypes.AttributeValueMemberS{Value: "TargetingProfile"}
	fake.getItem["TARGET#id-1"] = item

	resp, _ := handle(context.Background(), operatorReq(
		"PATCH", "/targeting/{id}", validBody(),
		map[string]string{"id": "id-1"},
		map[string]string{"If-Match": "etag-old"},
	))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var out targeting.Profile
	_ = json.Unmarshal([]byte(resp.Body), &out)
	if out.Etag == "etag-old" {
		t.Errorf("etag did not rotate: %q", out.Etag)
	}
}

func TestUpdate_EtagMismatch_Returns412(t *testing.T) {
	fake := reset(t)
	existing := targeting.Profile{ID: "id-1", Vertical: "x", Location: "y", Etag: "current"}
	item, _ := attributevalue.MarshalMap(existing)
	fake.getItem["TARGET#id-1"] = item
	fake.ccfeOnPut = true

	resp, _ := handle(context.Background(), operatorReq(
		"PATCH", "/targeting/{id}", validBody(),
		map[string]string{"id": "id-1"},
		map[string]string{"If-Match": "stale"},
	))
	if resp.StatusCode != 412 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

// --- DELETE /targeting/{id} ---------------------------------------------

func TestDelete_HappyPath_Returns204(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("DELETE", "/targeting/{id}", "", map[string]string{"id": "id-1"}, nil))
	if resp.StatusCode != 204 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestDelete_NotFound_Returns404(t *testing.T) {
	fake := reset(t)
	fake.delAbsent = true
	resp, _ := handle(context.Background(), operatorReq("DELETE", "/targeting/{id}", "", map[string]string{"id": "id-1"}, nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

// --- method routing ------------------------------------------------------

func TestMethodNotAllowed(t *testing.T) {
	reset(t)
	resp, _ := handle(context.Background(), operatorReq("PUT", "/targeting/{id}", "{}", map[string]string{"id": "id-1"}, nil))
	if resp.StatusCode != 405 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}
