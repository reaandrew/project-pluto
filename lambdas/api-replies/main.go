// Package main is the api-replies Lambda. Operator-only BFF for the
// reply-triage inbox (iter 8.5.3):
//
//	GET  /replies?status=<s>&category=<c>&limit=<n>&cursor=<c>
//	  — ReplyTriage items in triage-state <s> (default
//	    "operator_inbox"), newest first via gsi1, optional category
//	    filter, paginated.
//	POST /replies/{id}/reclassify   body {ref,newCategory,notes}
//	  — operator overrides the model's label; the item leaves the
//	    inbox (triageState="reviewed") and, for an attributed reply,
//	    the same Business.status side-effect the classifier would have
//	    applied is re-applied.
//
// ReplyTriage gsi1: gsi1pk="REPLYTRIAGE#STATUS#<state>",
// gsi1sk="<createdAt>#<id>" (written by lambdas/reply-triage). `ref`
// is a base64("pk|sk") the list returns so reclassify can address the
// exact item (the id alone can't locate the pk/sk; attributed items
// live under BUSINESS#<biz>, unattributed under REPLYTRIAGE#INBOX).
//
// Synchronous operator BFF — same shape as api-queue/api-email:
// auth.IsOperator gate, shared lambda_api role, no kill-switch /
// idempotency / cost-cap (not an event consumer).
//
// Spec note: .ralph/specs/08-admin-ui.md does not enumerate /replies
// (the .ralph tree is read-only); this BFF + the ReplyTriage shape it
// reads are driven by .ralph/fix_plan.md 8.5.3.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

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

const (
	defaultLimit = 50
	maxLimit     = 100
)

// triage states that can be listed; default is the operator inbox.
var allowedStates = map[string]bool{
	"operator_inbox": true,
	"auto_actioned":  true,
	"reviewed":       true,
}

var validCategories = map[string]bool{
	"unsubscribe":       true,
	"positive_interest": true,
	"unknown":           true,
}

// TriageRow mirrors lambdas/reply-triage's persisted item (the fields
// this BFF reads/writes).
type TriageRow struct {
	PK          string  `dynamodbav:"pk"`
	SK          string  `dynamodbav:"sk"`
	ID          string  `dynamodbav:"id"`
	BusinessID  string  `dynamodbav:"businessId"`
	DraftID     string  `dynamodbav:"draftId"`
	Category    string  `dynamodbav:"category"`
	Confidence  float64 `dynamodbav:"confidence"`
	Rationale   string  `dynamodbav:"rationale"`
	BodyExcerpt string  `dynamodbav:"bodyExcerpt"`
	TriageState string  `dynamodbav:"triageState"`
	CreatedAt   string  `dynamodbav:"createdAt"`
}

type replyItem struct {
	Ref         string  `json:"ref"`
	ID          string  `json:"id"`
	BusinessID  string  `json:"businessId,omitempty"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Rationale   string  `json:"rationale"`
	Excerpt     string  `json:"excerpt"`
	TriageState string  `json:"triageState"`
	CreatedAt   string  `json:"createdAt"`
}

type repliesResponse struct {
	Status     string      `json:"status"`
	Items      []replyItem `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}

	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	path := req.RequestContext.HTTP.Path
	switch {
	case method == "GET":
		return handleList(ctx, logger, req)
	case method == "POST" && strings.HasSuffix(path, "/reclassify"):
		return handleReclassify(ctx, logger, req)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

func handleList(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	q := req.QueryStringParameters
	state := q["status"]
	if state == "" {
		state = "operator_inbox"
	}
	if !allowedStates[state] {
		return httpresp.Error(400, fmt.Sprintf("unsupported status %q", state)), nil
	}
	category := q["category"]
	if category != "" && !validCategories[category] {
		return httpresp.Error(400, fmt.Sprintf("unsupported category %q", category)), nil
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

	items, next, err := listByState(ctx, state, category, int32(limit), startKey)
	if err != nil {
		logger.Error("api-replies.list failed", "err", err, "status", state)
		return httpresp.Error(500, "could not load replies"), nil
	}
	body, _ := json.Marshal(repliesResponse{Status: state, Items: items, NextCursor: next})
	return httpresp.JSON(200, string(body)), nil
}

func listByState(ctx context.Context, state, category string, limit int32, startKey map[string]dtypes.AttributeValue) ([]replyItem, string, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String(gsi1Index),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "REPLYTRIAGE#STATUS#" + state},
		},
		ScanIndexForward: aws.Bool(false), // newest first (gsi1sk = createdAt#id)
		Limit:            aws.Int32(limit),
	}
	if category != "" {
		in.FilterExpression = aws.String("category = :cat")
		in.ExpressionAttributeValues[":cat"] = &dtypes.AttributeValueMemberS{Value: category}
	}
	if startKey != nil {
		in.ExclusiveStartKey = startKey
	}
	out, err := client.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("query gsi1 %s: %w", state, err)
	}
	items := make([]replyItem, 0, len(out.Items))
	for _, raw := range out.Items {
		var r TriageRow
		if err := attributevalue.UnmarshalMap(raw, &r); err != nil {
			return nil, "", fmt.Errorf("unmarshal triage row: %w", err)
		}
		items = append(items, replyItem{
			Ref:         encodeRef(r.PK, r.SK),
			ID:          r.ID,
			BusinessID:  r.BusinessID,
			Category:    r.Category,
			Confidence:  r.Confidence,
			Rationale:   r.Rationale,
			Excerpt:     r.BodyExcerpt,
			TriageState: r.TriageState,
			CreatedAt:   r.CreatedAt,
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

type reclassifyBody struct {
	Ref         string `json:"ref"`
	NewCategory string `json:"newCategory"`
	Notes       string `json:"notes"`
}

func handleReclassify(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	var body reclassifyBody
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return httpresp.Error(400, "invalid JSON body"), nil
	}
	if !validCategories[body.NewCategory] {
		return httpresp.Error(400, fmt.Sprintf("unsupported newCategory %q", body.NewCategory)), nil
	}
	pk, sk, err := decodeRef(body.Ref)
	if err != nil {
		return httpresp.Error(400, "invalid ref"), nil
	}
	id := req.PathParameters["id"]
	if id != "" && !strings.HasSuffix(sk, "#"+id) && !strings.HasSuffix(sk, id) {
		return httpresp.Error(400, "ref does not match path id"), nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := reclassify(ctx, pk, sk, body.NewCategory, strings.TrimSpace(body.Notes), now); err != nil {
		logger.Error("api-replies.reclassify failed", "err", err)
		return httpresp.Error(500, "could not reclassify"), nil
	}

	// Re-apply the Business side-effect the classifier would have
	// applied for the chosen label (attributed replies only — we don't
	// store the prospect's address so SES suppression on reclassify is
	// out of scope; the auto path already suppressed at >=0.8).
	if biz := strings.TrimPrefix(pk, "BUSINESS#"); biz != pk {
		switch body.NewCategory {
		case "unsubscribe":
			if err := setStatus(ctx, biz, "rejected_after_review", now); err != nil {
				logger.Error("api-replies.setStatus failed", "err", err, "businessId", biz)
				return httpresp.Error(500, "reclassified but status update failed"), nil
			}
		case "positive_interest":
			if err := setStatus(ctx, biz, "responded", now); err != nil {
				logger.Error("api-replies.setStatus failed", "err", err, "businessId", biz)
				return httpresp.Error(500, "reclassified but status update failed"), nil
			}
		}
	}
	return httpresp.JSON(200, `{"status":"ok"}`), nil
}

func reclassify(ctx context.Context, pk, sk, newCategory, notes, now string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pk},
			"sk": &dtypes.AttributeValueMemberS{Value: sk},
		},
		UpdateExpression: aws.String("SET category = :c, triageState = :st, gsi1pk = :gpk, operatorAction = :oa, operatorNotes = :on, updatedAt = :ts"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":c":   &dtypes.AttributeValueMemberS{Value: newCategory},
			":st":  &dtypes.AttributeValueMemberS{Value: "reviewed"},
			":gpk": &dtypes.AttributeValueMemberS{Value: "REPLYTRIAGE#STATUS#reviewed"},
			":oa":  &dtypes.AttributeValueMemberS{Value: "reclassified"},
			":on":  &dtypes.AttributeValueMemberS{Value: notes},
			":ts":  &dtypes.AttributeValueMemberS{Value: now},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("reply-triage item not found")
		}
		return fmt.Errorf("update ReplyTriage: %w", err)
	}
	return nil
}

// setStatus flips Business.status + gsi1pk, guarded so a converted
// business is never regressed (mirrors reply-detector/reply-triage).
func setStatus(ctx context.Context, businessID, status, now string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
		UpdateExpression:         aws.String("SET #s = :s, gsi1pk = :pk, updatedAt = :ts"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":   &dtypes.AttributeValueMemberS{Value: status},
			":pk":  &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
			":ts":  &dtypes.AttributeValueMemberS{Value: now},
			":con": &dtypes.AttributeValueMemberS{Value: "converted"},
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND #s <> :con"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return nil // vanished or already converted — no-op
		}
		return fmt.Errorf("update business %s status: %w", businessID, err)
	}
	return nil
}

// ref = base64("pk|sk"). Opaque to the client; lets reclassify address
// the exact item without a second lookup.
func encodeRef(pk, sk string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(pk + "|" + sk))
}

func decodeRef(ref string) (pk, sk string, err error) {
	b, derr := base64.RawURLEncoding.DecodeString(ref)
	if derr != nil {
		return "", "", derr
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed ref")
	}
	return parts[0], parts[1], nil
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
