// Package main is the api-queue Lambda. Operator-only BFF for the
// review-queue list (iter 6.1):
//
//	GET /queue?status=<s>&limit=<n>&cursor=<c>
//	  — Businesses in review status <s> (default "awaiting_review"),
//	    ordered by priorityScore DESC via the gsi1 index, paginated.
//
// gsi1: gsi1pk = "BUSINESS#STATUS#<status>", gsi1sk =
// "<priorityScore:%.4f>#<businessId>" (queue.EncodeGSI1SK). Querying
// that partition with ScanIndexForward=false yields highest-priority-
// first, which is exactly the queue order the operator wants.
//
// Synchronous operator BFF — same shape as api-specs / api-website:
// auth.IsOperator gate, shared lambda_api role, no kill-switch /
// idempotency / cost-cap (not an event consumer).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

const gsi1Index = "gsi1"

// defaultLimit / maxLimit bound a single page. The UI's
// maxReviewQueueSize cap (PipelineSettings) is a presentation concern
// applied client-side in iter 6.2; the API stays generic.
const (
	defaultLimit = 50
	maxLimit     = 100
)

// allowedStatuses are the Business review states the queue can list.
// Anything else is rejected so a typo can't silently return an empty
// page (or scan an unexpected partition).
var allowedStatuses = map[string]bool{
	"awaiting_review":       true,
	"qualified":             true,
	"approved":              true,
	"rejected_after_review": true,
	"regenerate_requested":  true,
}

// BusinessRow is the gsi1-projected Business item (projection=ALL). We
// surface only what the queue card needs; priorityScore is parsed from
// gsi1sk ("<priority>#<businessId>") since the score itself lives on
// the Qualification item, not the Business.
type BusinessRow struct {
	ID            string `dynamodbav:"id"`
	Name          string `dynamodbav:"name"`
	Domain        string `dynamodbav:"domain"`
	Vertical      string `dynamodbav:"vertical"`
	Location      string `dynamodbav:"location"`
	Status        string `dynamodbav:"status"`
	LastAuditID   string `dynamodbav:"lastAuditId"`
	LastSpecID    string `dynamodbav:"lastSpecId"`
	LastWebsiteID string `dynamodbav:"lastWebsiteId"`
	DiscoveredAt  string `dynamodbav:"discoveredAt"`
	GSI1SK        string `dynamodbav:"gsi1sk"`
}

type queueItem struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Domain        string  `json:"domain"`
	Vertical      string  `json:"vertical"`
	Location      string  `json:"location"`
	Status        string  `json:"status"`
	PriorityScore float64 `json:"priorityScore"`
	LastAuditID   string  `json:"lastAuditId,omitempty"`
	LastSpecID    string  `json:"lastSpecId,omitempty"`
	LastWebsiteID string  `json:"lastWebsiteId,omitempty"`
	DiscoveredAt  string  `json:"discoveredAt,omitempty"`
}

type queueResponse struct {
	Status     string      `json:"status"`
	Items      []queueItem `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"` // opaque; pass back as ?cursor=
}

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}
	if strings.ToUpper(req.RequestContext.HTTP.Method) != "GET" {
		return httpresp.Error(405, "method not allowed"), nil
	}

	q := req.QueryStringParameters
	status := q["status"]
	if status == "" {
		status = "awaiting_review"
	}
	if !allowedStatuses[status] {
		return httpresp.Error(400, fmt.Sprintf("unsupported status %q", status)), nil
	}

	limit := defaultLimit
	if raw := q["limit"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return httpresp.Error(400, "limit must be a positive integer"), nil
		}
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}

	var startKey map[string]dtypes.AttributeValue
	if cur := q["cursor"]; cur != "" {
		sk, err := decodeCursor(cur)
		if err != nil {
			return httpresp.Error(400, "invalid cursor"), nil
		}
		startKey = sk
	}

	items, next, err := listByStatus(ctx, status, int32(limit), startKey)
	if err != nil {
		logger.Error("api-queue.listByStatus failed", "err", err, "status", status)
		return httpresp.Error(500, "could not load queue"), nil
	}

	body, _ := json.Marshal(queueResponse{Status: status, Items: items, NextCursor: next})
	return httpresp.JSON(200, string(body)), nil
}

// listByStatus runs the gsi1 query for one status partition, newest-
// highest-priority first, and returns the page + an opaque next cursor
// (empty when the partition is exhausted).
func listByStatus(ctx context.Context, status string, limit int32, startKey map[string]dtypes.AttributeValue) ([]queueItem, string, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String(gsi1Index),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
		},
		ScanIndexForward: aws.Bool(false), // highest priorityScore first
		Limit:            aws.Int32(limit),
	}
	if startKey != nil {
		in.ExclusiveStartKey = startKey
	}
	out, err := client.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("query gsi1 %s: %w", status, err)
	}
	items := make([]queueItem, 0, len(out.Items))
	for _, raw := range out.Items {
		var b BusinessRow
		if err := attributevalue.UnmarshalMap(raw, &b); err != nil {
			return nil, "", fmt.Errorf("unmarshal business row: %w", err)
		}
		items = append(items, queueItem{
			ID:            b.ID,
			Name:          b.Name,
			Domain:        b.Domain,
			Vertical:      b.Vertical,
			Location:      b.Location,
			Status:        b.Status,
			PriorityScore: priorityFromGSI1SK(b.GSI1SK),
			LastAuditID:   b.LastAuditID,
			LastSpecID:    b.LastSpecID,
			LastWebsiteID: b.LastWebsiteID,
			DiscoveredAt:  b.DiscoveredAt,
		})
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		next, err = encodeCursor(out.LastEvaluatedKey)
		if err != nil {
			return nil, "", fmt.Errorf("encode cursor: %w", err)
		}
	}
	return items, next, nil
}

// priorityFromGSI1SK parses the "<priority:%.4f>#<businessId>" sort key
// the qualifier writes (queue.EncodeGSI1SK). A malformed/empty sk
// degrades to 0 rather than failing the whole page.
func priorityFromGSI1SK(sk string) float64 {
	i := strings.IndexByte(sk, '#')
	if i <= 0 {
		return 0
	}
	f, err := strconv.ParseFloat(sk[:i], 64)
	if err != nil {
		return 0
	}
	return f
}

// Cursor: the gsi1 LastEvaluatedKey is exactly {pk, sk, gsi1pk,
// gsi1sk}, all String attributes — so we round-trip it as a base64'd
// JSON map[string]string. Opaque to the client.

func encodeCursor(key map[string]dtypes.AttributeValue) (string, error) {
	flat := make(map[string]string, len(key))
	for k, v := range key {
		s, ok := v.(*dtypes.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("cursor key %q is not a String attribute", k)
		}
		flat[k] = s.Value
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(cur string) (map[string]dtypes.AttributeValue, error) {
	b, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return nil, err
	}
	var flat map[string]string
	if err := json.Unmarshal(b, &flat); err != nil {
		return nil, err
	}
	if len(flat) == 0 {
		return nil, fmt.Errorf("empty cursor")
	}
	key := make(map[string]dtypes.AttributeValue, len(flat))
	for k, v := range flat {
		key[k] = &dtypes.AttributeValueMemberS{Value: v}
	}
	return key, nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
