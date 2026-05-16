// Package main is the metrics-rollup Lambda (iter 11.1 + 11.2/11.3
// extension). An hourly EventBridge-scheduled job that snapshots the
// pipeline funnel — globally AND per vertical — plus the day's spend
// and the live style/email-tone profile versions, writing ONE Metric
// item per UTC date for /metrics to read.
//
// Key: pk="METRIC", sk="DATE#<date>" — a single partition so the
// dashboard can range-query a date window (the iter-11.2/11.3
// requirement). Overwritten each run (idempotent; a same-hour retry
// just rewrites the snapshot). NOT kill-switch gated, no paid call.
//
// Per-vertical funnel powers the iter-11.3 vertical comparison
// (reply-rate = responded/emailed, conversion = converted/emailed) and
// the profile-version split (each day's rates are tagged with the
// style/tone versions in effect, so the operator can see whether a
// tuner-applied delta moved the numbers).
//
// Spec note: 02-data-model has the status set + CAP#<date> cost rows
// but no Metric item; .ralph is read-only so this shape is canonical,
// driven by .ralph/fix_plan.md 11.1–11.3.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
)

// FunnelStatuses is the ordered Business.status journey from
// .ralph/specs/02-data-model.md — the funnel the operator sees.
var FunnelStatuses = []string{
	"new", "auditing", "qualified", "rejected",
	"awaiting_review", "approved", "regenerate_requested",
	"rejected_after_review", "email_drafted", "emailed",
	"responded", "converted",
}

// seededVerticals always get a per-vertical block + profile versions
// even with zero businesses, so the dashboard shows the tuned
// verticals consistently.
var seededVerticals = []string{"default", "accountants"}

const metricTTLDays = 400

// VerticalMetric is one vertical's slice of the daily snapshot.
type VerticalMetric struct {
	Funnel       map[string]int `dynamodbav:"funnel" json:"funnel"`
	StyleVersion int            `dynamodbav:"styleVersion" json:"styleVersion"`
	ToneVersion  int            `dynamodbav:"toneVersion" json:"toneVersion"`
}

// MetricRow is the daily roll-up snapshot (pk=METRIC, sk=DATE#<date>).
type MetricRow struct {
	PK          string                     `dynamodbav:"pk"`
	SK          string                     `dynamodbav:"sk"`
	Type        string                     `dynamodbav:"type"`
	Date        string                     `dynamodbav:"date"`
	Funnel      map[string]int             `dynamodbav:"funnel"`
	PerVertical map[string]*VerticalMetric `dynamodbav:"perVertical"`
	CostByStage map[string]float64         `dynamodbav:"costByStage"`
	TotalCost   float64                    `dynamodbav:"totalCostUsd"`
	GeneratedAt string                     `dynamodbav:"generatedAt"`
	ExpiresAt   int64                      `dynamodbav:"expires_at"`
}

type runDeps struct {
	// StatusBreakdown returns the global count for a status and the
	// per-vertical split (vertical → count).
	StatusBreakdown func(ctx context.Context, status string) (total int, byVertical map[string]int, err error)
	ProfileVersions func(ctx context.Context, vertical string) (styleV, toneV int, err error)
	CostForDate     func(ctx context.Context, date string) (map[string]float64, error)
	PutMetric       func(ctx context.Context, m MetricRow) error
	Now             func() time.Time
}

func handle(ctx context.Context) error {
	deps, err := buildDeps(ctx)
	if err != nil {
		return err
	}
	return rollup(ctx, deps)
}

func rollup(ctx context.Context, d runDeps) error {
	logger := applog.FromContext(ctx)
	now := d.Now().UTC()
	date := now.Format("2006-01-02")

	funnel := make(map[string]int, len(FunnelStatuses))
	perVertical := map[string]*VerticalMetric{}
	ensure := func(v string) *VerticalMetric {
		if perVertical[v] == nil {
			perVertical[v] = &VerticalMetric{Funnel: map[string]int{}}
		}
		return perVertical[v]
	}
	for _, v := range seededVerticals {
		ensure(v)
	}

	for _, s := range FunnelStatuses {
		total, byVert, err := d.StatusBreakdown(ctx, s)
		if err != nil {
			return fmt.Errorf("status breakdown %s: %w", s, err)
		}
		funnel[s] = total
		for v, n := range byVert {
			ensure(v).Funnel[s] += n
		}
	}

	for v, vm := range perVertical {
		sv, tv, err := d.ProfileVersions(ctx, v)
		if err != nil {
			return fmt.Errorf("profile versions %s: %w", v, err)
		}
		vm.StyleVersion, vm.ToneVersion = sv, tv
	}

	costByStage, err := d.CostForDate(ctx, date)
	if err != nil {
		return fmt.Errorf("cost for %s: %w", date, err)
	}
	var total float64
	for _, v := range costByStage {
		total += v
	}

	m := MetricRow{
		PK:          "METRIC",
		SK:          "DATE#" + date,
		Type:        "Metric",
		Date:        date,
		Funnel:      funnel,
		PerVertical: perVertical,
		CostByStage: costByStage,
		TotalCost:   total,
		GeneratedAt: now.Format(time.RFC3339),
		ExpiresAt:   now.AddDate(0, 0, metricTTLDays).Unix(),
	}
	if err := d.PutMetric(ctx, m); err != nil {
		return fmt.Errorf("put metric %s: %w", date, err)
	}
	logger.Info("metrics_rollup.done", "date", date,
		"converted", funnel["converted"], "verticals", len(perVertical), "totalCostUsd", total)
	return nil
}

// --- AWS wiring -------------------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	if _, err := awsconfig.LoadDefaultConfig(ctx); err != nil {
		return runDeps{}, fmt.Errorf("metrics-rollup: AWS config: %w", err)
	}
	return runDeps{
		StatusBreakdown: statusBreakdown,
		ProfileVersions: profileVersions,
		CostForDate:     costForDate,
		PutMetric:       putMetric,
		Now:             time.Now,
	}, nil
}

// statusBreakdown queries the gsi1 status partition projecting only
// `vertical` (a tiny attribute — keeps transfer minimal while still
// giving the per-vertical split COUNT can't).
func statusBreakdown(ctx context.Context, status string) (int, map[string]int, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return 0, nil, err
	}
	total := 0
	byVert := map[string]int{}
	var lastKey map[string]dtypes.AttributeValue
	for {
		out, err := client.Query(ctx, &dynamodb.QueryInput{
			TableName:                aws.String(ddb.TableName()),
			IndexName:                aws.String("gsi1"),
			KeyConditionExpression:   aws.String("gsi1pk = :pk"),
			ProjectionExpression:     aws.String("#v"),
			ExpressionAttributeNames: map[string]string{"#v": "vertical"},
			ExpressionAttributeValues: map[string]dtypes.AttributeValue{
				":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("query gsi1 %s: %w", status, err)
		}
		for _, it := range out.Items {
			total++
			v := "default"
			if av, ok := it["vertical"].(*dtypes.AttributeValueMemberS); ok && av.Value != "" {
				v = av.Value
			}
			byVert[v]++
		}
		if len(out.LastEvaluatedKey) == 0 {
			return total, byVert, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

func profileVersions(ctx context.Context, vertical string) (int, int, error) {
	sv := 0
	if g, err := style.Get(ctx, vertical); err == nil {
		sv = g.Version
	} else if !errors.Is(err, style.ErrNotFound) {
		return 0, 0, fmt.Errorf("style.Get %s: %w", vertical, err)
	}
	tv := 0
	if p, err := tone.Get(ctx, vertical); err == nil {
		tv = p.Version
	} else if !errors.Is(err, tone.ErrNotFound) {
		return 0, 0, fmt.Errorf("tone.Get %s: %w", vertical, err)
	}
	return sv, tv, nil
}

func costForDate(ctx context.Context, date string) (map[string]float64, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "CAP#" + date},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("query CAP#%s: %w", date, err)
	}
	byStage := map[string]float64{}
	for _, it := range out.Items {
		var r cost.CostRecord
		if err := attributevalue.UnmarshalMap(it, &r); err != nil {
			return nil, fmt.Errorf("unmarshal cost record: %w", err)
		}
		if r.Stage != "" {
			byStage[r.Stage] += r.SpentUsd
		}
	}
	return byStage, nil
}

func putMetric(ctx context.Context, m MetricRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(m)
	if err != nil {
		return fmt.Errorf("marshal Metric: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()), Item: item,
	}); err != nil {
		return fmt.Errorf("put Metric: %w", err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
