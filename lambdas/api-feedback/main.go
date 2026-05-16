// Package main is the api-feedback Lambda. Operator-only BFF for the
// iter 9.2 Feedback log:
//
//	GET /feedback?vertical=<v>&subject=<s>&limit=<n>&cursor=<c>
//	  — Feedback rows for vertical <v> (default "default"),
//	    newest-first, optional stage(subject) filter, cursor-paged.
//
// Feedback rows (pkg/feedback): pk="FEEDBACK#<vertical>",
// sk="<createdAt>#<id>" — querying that partition with
// ScanIndexForward=false is a clean reverse-chronological log per
// vertical (gsi2's sk is subject-prefixed, so the base table gives
// better time ordering for a log view). The originalPayload /
// editedPayload bodies are deliberately NOT returned by the list —
// they can be large and (for email) contain a redacted draft; the
// log view only needs the who/what/when summary.
//
// Synchronous operator BFF — same shape as api-queue/api-replies:
// auth.IsOperator gate, shared lambda_api role, no kill-switch /
// idempotency / cost-cap (not an event consumer).
//
// Spec note: .ralph/specs/08-admin-ui.md does not enumerate /feedback
// (the .ralph tree is read-only); this BFF is driven by
// .ralph/fix_plan.md 9.2.
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

const (
	defaultLimit = 50
	maxLimit     = 100
)

// allowedSubjects mirrors pkg/feedback's closed Subject set so a typo
// can't silently filter to nothing.
var allowedSubjects = map[string]bool{
	"audit":         true,
	"qualification": true,
	"spec":          true,
	"website":       true,
	"email":         true,
}

// FeedbackRow is the projection this BFF reads — note it never selects
// originalPayload/editedPayload (kept off the log view).
type FeedbackRow struct {
	ID         string `dynamodbav:"id"`
	Subject    string `dynamodbav:"subject"`
	SubjectID  string `dynamodbav:"subjectId"`
	BusinessID string `dynamodbav:"businessId"`
	Actor      string `dynamodbav:"actor"`
	Action     string `dynamodbav:"action"`
	Notes      string `dynamodbav:"notes"`
	Vertical   string `dynamodbav:"vertical"`
	CreatedAt  string `dynamodbav:"createdAt"`
}

type feedbackItem struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	SubjectID  string `json:"subjectId"`
	BusinessID string `json:"businessId,omitempty"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Notes      string `json:"notes,omitempty"`
	Vertical   string `json:"vertical"`
	CreatedAt  string `json:"createdAt"`
}

type feedbackResponse struct {
	Vertical   string         `json:"vertical"`
	Items      []feedbackItem `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
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
	vertical := q["vertical"]
	if vertical == "" {
		vertical = "default"
	}
	subject := q["subject"]
	if subject != "" && !allowedSubjects[subject] {
		return httpresp.Error(400, fmt.Sprintf("unsupported subject %q", subject)), nil
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

	items, next, err := listByVertical(ctx, vertical, subject, int32(limit), startKey)
	if err != nil {
		logger.Error("api-feedback.list failed", "err", err, "vertical", vertical)
		return httpresp.Error(500, "could not load feedback"), nil
	}
	body, _ := json.Marshal(feedbackResponse{Vertical: vertical, Items: items, NextCursor: next})
	return httpresp.JSON(200, string(body)), nil
}

func listByVertical(ctx context.Context, vertical, subject string, limit int32, startKey map[string]dtypes.AttributeValue) ([]feedbackItem, string, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "FEEDBACK#" + vertical},
		},
		ScanIndexForward: aws.Bool(false), // newest first (sk = createdAt#id)
		Limit:            aws.Int32(limit),
	}
	if subject != "" {
		in.FilterExpression = aws.String("subject = :subj")
		in.ExpressionAttributeValues[":subj"] = &dtypes.AttributeValueMemberS{Value: subject}
	}
	if startKey != nil {
		in.ExclusiveStartKey = startKey
	}
	out, err := client.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("query feedback %s: %w", vertical, err)
	}
	items := make([]feedbackItem, 0, len(out.Items))
	for _, raw := range out.Items {
		var r FeedbackRow
		if err := attributevalue.UnmarshalMap(raw, &r); err != nil {
			return nil, "", fmt.Errorf("unmarshal feedback row: %w", err)
		}
		items = append(items, feedbackItem{
			ID:         r.ID,
			Subject:    r.Subject,
			SubjectID:  r.SubjectID,
			BusinessID: r.BusinessID,
			Actor:      r.Actor,
			Action:     r.Action,
			Notes:      r.Notes,
			Vertical:   r.Vertical,
			CreatedAt:  r.CreatedAt,
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
