// Package main is the api-settings Lambda. Two routes:
//
//	GET   /settings  → return current PipelineSettings
//	PATCH /settings  → partial update of top-level fields, returns the merged row
//
// API Gateway's JWT authorizer (terraform/cognito.tf) verifies the
// caller's Cognito ID token; this handler additionally enforces the
// "operator" group claim — see pkg/auth.IsOperator. Without that
// claim the handler returns 403 regardless of which route was hit.
//
// PATCH semantics: the request body is a JSON object whose top-level
// keys are a subset of {pipelineEnabled, stages, caps, thresholds,
// budgets}. Unknown top-level keys cause a 400. Each value is merged
// onto the current row field-by-field — sending
// {"caps":{"maxAuditsPerDay":99}} updates only that one cap and leaves
// the others alone. To clear a numeric field, send it explicitly with
// value 0; an absent field is treated as "no change".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}

	switch strings.ToUpper(req.RequestContext.HTTP.Method) {
	case "GET":
		return handleGet(ctx, logger)
	case "PATCH":
		return handlePatch(ctx, logger, req.Body)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

func handleGet(ctx context.Context, logger *slog.Logger) (events.APIGatewayV2HTTPResponse, error) {
	s, err := killswitch.Get(ctx)
	if err != nil {
		logger.Error("settings.get failed", "err", err)
		return httpresp.Error(500, "could not read settings"), nil
	}
	body, err := json.Marshal(s)
	if err != nil {
		logger.Error("settings.marshal failed", "err", err)
		return httpresp.Error(500, "could not encode settings"), nil
	}
	return httpresp.JSON(200, string(body)), nil
}

// allowedPatchKeys is the closed set of top-level fields that PATCH may
// update. Anything else in the body is rejected with 400 — this stops a
// caller from accidentally writing junk attributes onto the singleton row.
var allowedPatchKeys = map[string]bool{
	"pipelineEnabled": true,
	"stages":          true,
	"caps":            true,
	"thresholds":      true,
	"budgets":         true,
}

func handlePatch(ctx context.Context, logger *slog.Logger, body string) (events.APIGatewayV2HTTPResponse, error) {
	if strings.TrimSpace(body) == "" {
		return httpresp.Error(400, "empty body"), nil
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &patch); err != nil {
		return httpresp.Error(400, "invalid JSON: "+err.Error()), nil
	}
	if len(patch) == 0 {
		return httpresp.Error(400, "no fields to update"), nil
	}
	for k := range patch {
		if !allowedPatchKeys[k] {
			return httpresp.Error(400, fmt.Sprintf("unknown field %q", k)), nil
		}
	}

	current, err := readFresh(ctx)
	if err != nil {
		logger.Error("settings.read failed", "err", err)
		return httpresp.Error(500, "could not read current settings"), nil
	}
	if err := applyPatch(&current, patch); err != nil {
		return httpresp.Error(400, err.Error()), nil
	}
	if err := writeRow(ctx, current); err != nil {
		logger.Error("settings.write failed", "err", err)
		return httpresp.Error(500, "could not write settings"), nil
	}
	// Local cache must reflect the write so a follow-up GET on this same
	// warm container returns the new value rather than waiting for the
	// 60s TTL to elapse.
	killswitch.SetSettings(&current)
	logger.Info("settings.updated", "fields", keysOf(patch))

	body2, err := json.Marshal(current)
	if err != nil {
		logger.Error("settings.marshal failed", "err", err)
		return httpresp.Error(500, "could not encode settings"), nil
	}
	return httpresp.JSON(200, string(body2)), nil
}

// applyPatch unmarshals each present top-level field of patch onto dst.
// Because dst is already populated from DynamoDB, json.Unmarshal only
// overwrites fields that appear in the JSON — fields that are absent
// keep their current value, giving a deep merge for free.
func applyPatch(dst *killswitch.Settings, patch map[string]json.RawMessage) error {
	if raw, ok := patch["pipelineEnabled"]; ok {
		if err := json.Unmarshal(raw, &dst.PipelineEnabled); err != nil {
			return fmt.Errorf("pipelineEnabled: %w", err)
		}
	}
	if raw, ok := patch["stages"]; ok {
		if err := json.Unmarshal(raw, &dst.Stages); err != nil {
			return fmt.Errorf("stages: %w", err)
		}
	}
	if raw, ok := patch["caps"]; ok {
		if err := json.Unmarshal(raw, &dst.Caps); err != nil {
			return fmt.Errorf("caps: %w", err)
		}
	}
	if raw, ok := patch["thresholds"]; ok {
		if err := json.Unmarshal(raw, &dst.Thresholds); err != nil {
			return fmt.Errorf("thresholds: %w", err)
		}
	}
	if raw, ok := patch["budgets"]; ok {
		if err := json.Unmarshal(raw, &dst.Budgets); err != nil {
			return fmt.Errorf("budgets: %w", err)
		}
	}
	return nil
}

// readFresh reads the singleton settings row directly from DynamoDB,
// bypassing the in-process killswitch cache. PATCH must not read a
// stale cached view, otherwise concurrent edits could lose updates.
func readFresh(ctx context.Context) (killswitch.Settings, error) {
	var s killswitch.Settings
	client, err := ddb.Client(ctx)
	if err != nil {
		return s, err
	}
	table := ddb.TableName()
	if table == "" {
		return s, errors.New("ITEMS_TABLE not set")
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: killswitch.SettingsPK},
			"sk": &dtypes.AttributeValueMemberS{Value: killswitch.SettingsSK},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return s, fmt.Errorf("GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return s, fmt.Errorf("PipelineSettings row not found at %s/%s", killswitch.SettingsPK, killswitch.SettingsSK)
	}
	if err := attributevalue.UnmarshalMap(out.Item, &s); err != nil {
		return s, fmt.Errorf("unmarshal: %w", err)
	}
	return s, nil
}

func writeRow(ctx context.Context, s killswitch.Settings) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(s)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsPK}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsSK}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("PutItem: %w", err)
	}
	return nil
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
