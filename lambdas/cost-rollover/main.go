// Package main is the daily cost-rollover Lambda. Triggered by an
// EventBridge Scheduler rule at 00:05 UTC every day, it re-enables any
// stages whose pause reason is "budget" — stages that pkg/cost paused
// the previous day after hitting their daily-spend cap.
//
// What it does NOT do:
//   - Reset spend counters. Counters are bucketed by `pk=CAP#YYYY-MM-DD`,
//     so a new day naturally starts at zero; the previous day's row
//     ages out via the 30-day TTL on `expires_at`.
//   - Re-enable stages an operator manually disabled via /settings.
//     Those carry no pause reason; the rollover ignores them.
//   - Re-enable stages whose pause reason is unrecognised. Future
//     auto-pause sources (e.g. quota exhaustion) need explicit handling
//     here when they're introduced.
//
// Idempotent by construction: a no-op run produces no DDB writes and
// emits a single `pipeline.rollover.noop` log line.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

func handle(ctx context.Context, _ any) error {
	logger := applog.FromContext(ctx)

	current, err := readFresh(ctx)
	if err != nil {
		logger.Error("rollover.read failed", "err", err)
		return err
	}

	updated, reenabled := rollover(current)
	if len(reenabled) == 0 {
		logger.Info("pipeline.rollover.noop")
		return nil
	}

	if err := writeRow(ctx, updated); err != nil {
		logger.Error("rollover.write failed", "err", err, "stages", reenabled)
		return err
	}

	logger.Info("pipeline.rollover.completed",
		"stages_reenabled", reenabled,
		"count", len(reenabled),
		"metric", "pipeline.rollover.completed",
	)
	// Wipe any cached settings the warm container picked up before this
	// invocation so a follow-up Lambda invocation in the same container
	// (rare for a cron Lambda but possible) sees the post-rollover view.
	killswitch.SetSettings(&updated)
	return nil
}

// rollover is the pure decision function: given current settings, returns
// the rolled-over settings and the list of stages re-enabled. Pure so it
// is exhaustively unit-testable without DDB. Returns an empty slice (not
// nil) when nothing needs re-enabling — keeps caller logic simple.
func rollover(in killswitch.Settings) (killswitch.Settings, []string) {
	out := in
	reenabled := []string{}

	if !in.Stages.DiscoveryEnabled && in.StagePauseReasons.Discovery == killswitch.PauseReasonBudget {
		out.Stages.DiscoveryEnabled = true
		out.StagePauseReasons.Discovery = ""
		reenabled = append(reenabled, killswitch.StageDiscovery)
	}
	if !in.Stages.AuditEnabled && in.StagePauseReasons.Audit == killswitch.PauseReasonBudget {
		out.Stages.AuditEnabled = true
		out.StagePauseReasons.Audit = ""
		reenabled = append(reenabled, killswitch.StageAudit)
	}
	if !in.Stages.PreviewEnabled && in.StagePauseReasons.Preview == killswitch.PauseReasonBudget {
		out.Stages.PreviewEnabled = true
		out.StagePauseReasons.Preview = ""
		reenabled = append(reenabled, killswitch.StagePreview)
	}
	if !in.Stages.OutreachEnabled && in.StagePauseReasons.Outreach == killswitch.PauseReasonBudget {
		out.Stages.OutreachEnabled = true
		out.StagePauseReasons.Outreach = ""
		reenabled = append(reenabled, killswitch.StageOutreach)
	}

	return out, reenabled
}

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

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
