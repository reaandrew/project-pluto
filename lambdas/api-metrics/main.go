// Package main is the api-metrics Lambda. Two routes today:
//
//	GET  /metrics/discoveries        → recent Business rows + a 7-day daily count
//	POST /metrics/discoveries/run    → synchronously invokes the discover Lambda
//
// Iter 1.4 is the operator-visible end of the iter-1 discovery
// chain — operators see what discovery has actually produced and
// can manually kick off a run without waiting for the hourly
// schedule. The /metrics page in the spec (08-admin-ui.md § "Metrics")
// has more (funnel, reply-rate, approve-rate, spend) — those
// land alongside their producing iters (audit funnel in 2, reply
// in 8, etc.). This Lambda just covers the discoveries widget.
//
// Operator-only via the standard Cognito JWT + group-claim gate.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

// LambdaInvoker is the subset of *lambda.Client used to fire the
// discover Lambda. Tests inject a fake.
type LambdaInvoker interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// handlerDeps is the testable surface — handler() builds it from
// env vars + SDKs in production; tests inject fakes for each.
type handlerDeps struct {
	DiscoverFunctionName string
	Invoker              LambdaInvoker
	Now                  func() time.Time
}

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}

	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	path := req.RequestContext.HTTP.Path
	// RouteKey already carries the path template (e.g. "GET /metrics/discoveries");
	// for parsing simplicity we look at the literal path tail.
	switch {
	case method == "GET" && strings.HasSuffix(path, "/metrics/discoveries"):
		_ = logger // logger used downstream; keep for future audit fields here
		return handleDiscoveries(ctx)
	case method == "POST" && strings.HasSuffix(path, "/metrics/discoveries/run"):
		deps, err := buildDeps(ctx)
		if err != nil {
			logger.Error("metrics.builddeps failed", "err", err)
			return httpresp.Error(500, "could not initialise"), nil
		}
		return handleRunDiscovery(ctx, deps)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

// handleDiscoveries returns recent businesses (status=new) plus a
// 7-day daily count. The frontend widget renders both.
func handleDiscoveries(ctx context.Context) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)
	rows, err := queryRecent(ctx)
	if err != nil {
		logger.Error("metrics.query failed", "err", err)
		return httpresp.Error(500, "could not query discoveries"), nil
	}
	resp := discoveriesResponse{
		Recent:        rows,
		CountsByDay:   countsByDay(rows, time.Now().UTC()),
		TotalLast7Day: totalLast7Days(rows, time.Now().UTC()),
	}
	body, _ := json.Marshal(resp)
	return httpresp.JSON(200, string(body)), nil
}

// handleRunDiscovery does a synchronous Invoke of the discover
// Lambda. Synchronous so the operator sees success/failure in the
// UI immediately; the underlying handler is already idempotent
// (per-domain dedup), so a re-run from the schedule firing
// concurrently is safe.
func handleRunDiscovery(ctx context.Context, deps handlerDeps) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)
	if deps.DiscoverFunctionName == "" {
		return httpresp.Error(500, "DISCOVER_FUNCTION_NAME env var not set"), nil
	}
	out, err := deps.Invoker.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(deps.DiscoverFunctionName),
		InvocationType: lambdatypes.InvocationTypeRequestResponse,
		Payload:        []byte(`{"source":"manual","trigger":"api-metrics"}`),
	})
	if err != nil {
		logger.Error("metrics.invoke failed", "err", err)
		return httpresp.Error(502, "discover Lambda invoke failed: "+err.Error()), nil
	}
	// The discover Lambda itself returns no payload on success; a
	// non-nil FunctionError means it returned an error. Surface it
	// so the operator sees it without grepping CloudWatch.
	if out.FunctionError != nil && *out.FunctionError != "" {
		payload := string(out.Payload)
		logger.Error("metrics.invoke functionError",
			"functionError", *out.FunctionError, "payload", payload)
		return httpresp.Error(502, "discover Lambda failed: "+payload), nil
	}
	logger.Info("metrics.invoke ok", "status", out.StatusCode)
	body, _ := json.Marshal(runDiscoveryResponse{
		Status:    "ok",
		StartedAt: deps.Now().UTC().Format(time.RFC3339),
	})
	return httpresp.JSON(202, string(body)), nil
}

// queryRecent reads up to 50 recent Business rows in status=new
// via gsi1 (BUSINESS#STATUS#new) sorted by priority+id descending.
// 50 is sufficient for the operator's at-a-glance widget;
// pagination + filters land with /businesses in a later iter.
func queryRecent(ctx context.Context) ([]businessRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#new"},
		},
		ScanIndexForward: aws.Bool(false), // newest first
		Limit:            aws.Int32(50),
	})
	if err != nil {
		return nil, fmt.Errorf("query gsi1: %w", err)
	}
	rows := make([]businessRow, 0, len(out.Items))
	for _, item := range out.Items {
		var r businessRow
		if err := attributevalue.UnmarshalMap(item, &r); err != nil {
			return nil, fmt.Errorf("unmarshal row: %w", err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// countsByDay buckets the recent rows by createdAt date for the
// last 7 days. Exposed for testing.
func countsByDay(rows []businessRow, now time.Time) map[string]int {
	out := make(map[string]int, 7)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		out[d] = 0
	}
	for _, r := range rows {
		t, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err != nil {
			continue
		}
		key := t.UTC().Format("2006-01-02")
		if _, ok := out[key]; ok {
			out[key]++
		}
	}
	return out
}

func totalLast7Days(rows []businessRow, now time.Time) int {
	earliest := now.AddDate(0, 0, -6).Truncate(24 * time.Hour)
	count := 0
	for _, r := range rows {
		t, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err == nil && !t.Before(earliest) {
			count++
		}
	}
	return count
}

func buildDeps(ctx context.Context) (handlerDeps, error) {
	name := os.Getenv("DISCOVER_FUNCTION_NAME")
	if name == "" {
		return handlerDeps{}, fmt.Errorf("DISCOVER_FUNCTION_NAME is not set")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return handlerDeps{}, fmt.Errorf("loading AWS config: %w", err)
	}
	return handlerDeps{
		DiscoverFunctionName: name,
		Invoker:              awslambda.NewFromConfig(cfg),
		Now:                  time.Now,
	}, nil
}

// businessRow is the subset of the Business item shape the metrics
// widget surfaces — keeps the response payload small. Schema
// mirrors lambdas/discover/main.go's businessRow.
type businessRow struct {
	ID         string  `json:"id"         dynamodbav:"id"`
	Name       string  `json:"name"       dynamodbav:"name"`
	Domain     string  `json:"domain"     dynamodbav:"domain"`
	Vertical   string  `json:"vertical"   dynamodbav:"vertical"`
	Location   string  `json:"location"   dynamodbav:"location"`
	Source     string  `json:"source"     dynamodbav:"source"`
	Confidence float64 `json:"confidence" dynamodbav:"confidence"`
	Status     string  `json:"status"     dynamodbav:"status"`
	CreatedAt  string  `json:"createdAt"  dynamodbav:"createdAt"`
}

type discoveriesResponse struct {
	Recent        []businessRow  `json:"recent"`
	CountsByDay   map[string]int `json:"countsByDay"`
	TotalLast7Day int            `json:"totalLast7Day"`
}

type runDiscoveryResponse struct {
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
