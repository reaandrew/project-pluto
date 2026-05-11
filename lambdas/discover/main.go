// Package main is the discover Lambda. EventBridge Scheduler fires
// it hourly; on each fire it iterates every ENABLED TargetingProfile,
// queries each enabled provider (CSV / Companies House / Google
// Places), dedupes results by lowercased domain via the items
// table's gsi3 index, persists new Business rows, and publishes
// `business.found` events.
//
// Three load-bearing pkg wrappers:
//
//   - killswitch.WithKillSwitch at the entry point. When the master
//     pipelineEnabled or stages.discoveryEnabled flag is off the
//     handler logs a skipped_killed metric and returns nil
//     immediately. Cost-rollover (iter 0.F.4) re-enables stages
//     whose pause reason is "budget" at 00:05 UTC daily.
//
//   - cost.WithCostCap around the Google Places provider call.
//     Estimate is the provider's CostPerCallUSD; cap is the daily
//     places budget from PipelineSettings.Budgets.DailyPlacesUsd.
//     ErrBudgetCapExceeded → skip Places for the rest of this run
//     and the cost-rollover Lambda flips the stage back on tomorrow.
//
//   - Per-domain dedup via gsi3 (DOMAIN#<lowercased>). A business
//     surfaced by multiple providers in one run gets ONE
//     `business.found`; subsequent runs find the existing Business
//     row and short-circuit. This is the "idempotent on lowercased
//     domain" requirement from .ralph/fix_plan.md item 1.3.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/discover/providers/companieshouse"
	csvprov "github.com/reaandrew/ai-website-agency/lambdas/discover/providers/csv"
	"github.com/reaandrew/ai-website-agency/lambdas/discover/providers/googleplaces"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

const consumerName = "discover"

// runDeps holds the dependencies the run() function needs. Exposed
// as a struct so tests can inject fakes for each — the production
// `handle` wires real implementations from env + AWS SDKs.
type runDeps struct {
	ListProfiles func(ctx context.Context) ([]targeting.Profile, error)
	Providers    []discovery.Provider
	Publish      func(ctx context.Context, env events.Envelope[BusinessFoundDetail]) error
	BudgetUSD    func(ctx context.Context, stage string) (float64, error)
	Now          func() time.Time
	NewBizID     func() string
}

func handle(ctx context.Context, _ any) error {
	return killswitch.WithKillSwitch(ctx, killswitch.StageDiscovery, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		return run(ctx, deps)
	})
}

// run is the testable core. It takes everything it needs as a
// struct so unit tests can avoid the env + SDK wiring entirely.
func run(ctx context.Context, d runDeps) error {
	logger := applog.FromContext(ctx)

	profiles, err := d.ListProfiles(ctx)
	if err != nil {
		return fmt.Errorf("discover: list profiles: %w", err)
	}
	enabled := make([]targeting.Profile, 0, len(profiles))
	for _, p := range profiles {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}
	logger.Info("discover.run", "enabled_profiles", len(enabled), "providers", len(d.Providers))

	// Track which providers have been disabled mid-run by a
	// per-call cap exceeded. A subsequent profile would re-attempt
	// the same provider otherwise.
	skipProvider := map[string]bool{}

	for _, profile := range enabled {
		for _, provider := range d.Providers {
			if skipProvider[provider.ID()] {
				continue
			}
			if err := runOne(ctx, logger, d, profile, provider); err != nil {
				if errors.Is(err, cost.ErrBudgetCapExceeded) {
					logger.Info("discover.provider.skipped_capped",
						"provider", provider.ID(),
						"metric", "pipeline.places.skipped_capped",
					)
					skipProvider[provider.ID()] = true
					continue
				}
				logger.Error("discover.provider.failed",
					"provider", provider.ID(),
					"profile", profile.ID,
					"err", err,
				)
				// Don't fail the whole run — one provider's
				// outage shouldn't lose work from the others.
			}
		}
	}
	return nil
}

// runOne queries one (profile, provider) pair. Wraps the provider
// call in cost.WithCostCap when the provider has a non-zero
// per-call cost — places gets the budget cap; CSV + Companies
// House are free and bypass.
func runOne(
	ctx context.Context,
	logger *slog.Logger,
	d runDeps,
	profile targeting.Profile,
	provider discovery.Provider,
) error {
	req := discovery.FindRequest{
		Vertical:        profile.Vertical,
		Location:        profile.Location,
		IncludeKeywords: profile.IncludeKeywords,
		ExcludeKeywords: profile.ExcludeKeywords,
	}
	const perCallCap = 25 // small batch — caller decides daily cap

	var (
		results []discovery.DiscoveredBusiness
		err     error
	)
	if provider.CostPerCallUSD() > 0 {
		// Paid path — cap against PipelineSettings.Budgets.DailyPlacesUsd.
		stageKey := killswitch.StagePlaces // places is the only paid provider today
		capUsd, capErr := d.BudgetUSD(ctx, stageKey)
		if capErr != nil {
			return fmt.Errorf("budget lookup: %w", capErr)
		}
		results, err = cost.WithCostCap(
			ctx, stageKey, provider.CostPerCallUSD(), capUsd,
			func(ctx context.Context) ([]discovery.DiscoveredBusiness, float64, error) {
				r, e := provider.Find(ctx, req, perCallCap)
				if e != nil {
					return nil, 0, e
				}
				return r, provider.CostPerCallUSD(), nil
			},
		)
	} else {
		results, err = provider.Find(ctx, req, perCallCap)
	}
	if err != nil {
		return err
	}

	for _, biz := range results {
		if err := persistOne(ctx, logger, d, profile, biz); err != nil {
			logger.Error("discover.persist.failed",
				"name", biz.Name, "domain", biz.Domain, "err", err)
		}
	}
	return nil
}

// persistOne writes one DiscoveredBusiness as a Business row and
// publishes business.found. Returns nil + logs when the domain
// already exists (the duplicate path is expected and not an error).
//
// Skip when domain is empty — Companies House results often have
// no domain. Iter 2's audit Lambda is responsible for resolving
// domains for CH-sourced businesses; until then we can't
// deduplicate them, so we skip the write to keep the dataset
// clean. A future iter wires CH-without-domain into a separate
// "needs domain resolution" queue.
func persistOne(
	ctx context.Context,
	logger *slog.Logger,
	d runDeps,
	profile targeting.Profile,
	biz discovery.DiscoveredBusiness,
) error {
	domain := strings.ToLower(strings.TrimSpace(biz.Domain))
	if domain == "" {
		// Companies House case — log and skip; audit resolves later.
		logger.Debug("discover.skip.no_domain",
			"source", biz.Source, "name", biz.Name)
		return nil
	}

	exists, err := domainExists(ctx, domain)
	if err != nil {
		return fmt.Errorf("dedup check: %w", err)
	}
	if exists {
		logger.Debug("discover.dedup", "domain", domain)
		return nil
	}

	now := d.Now().UTC().Format(time.RFC3339)
	id := d.NewBizID()
	row := businessRow{
		PK:         "BUSINESS#" + id,
		SK:         "PROFILE",
		Type:       "Business",
		ID:         id,
		Name:       biz.Name,
		Domain:     domain,
		Vertical:   biz.Vertical,
		Location:   biz.Location,
		Source:     biz.Source,
		SourceRefs: biz.SourceRefs,
		Status:     "new",
		Confidence: biz.Confidence,
		CreatedAt:  now,
		UpdatedAt:  now,
		Etag:       randomHex(16),
		GSI1PK:     "BUSINESS#STATUS#new",
		GSI1SK:     fmt.Sprintf("%.4f#%s", biz.Confidence, id),
		GSI2PK:     "BUSINESS#VERTICAL#" + biz.Vertical,
		GSI2SK:     now,
		GSI3PK:     "DOMAIN#" + domain,
		GSI3SK:     "PROFILE",
	}
	if err := writeBusiness(ctx, row); err != nil {
		return err
	}

	// Emit business.found. Failure here is logged but not surfaced
	// as the run's error — the row is already persisted; the
	// downstream consumer can recover by replaying from gsi1
	// (BUSINESS#STATUS#new).
	env := events.New("business.found", consumerName, BusinessFoundDetail{
		BusinessID: id,
		Domain:     domain,
		Name:       biz.Name,
		Vertical:   biz.Vertical,
		Location:   biz.Location,
		Source:     biz.Source,
		ProfileID:  profile.ID,
	})
	if err := d.Publish(ctx, env); err != nil {
		logger.Error("discover.publish.failed",
			"businessId", id, "err", err)
	}
	return nil
}

// BusinessFoundDetail is the `detail` payload of the business.found
// event. Shape from .ralph/specs/03-events.md (extended with
// profileId so the qualifier can backreference which target
// produced the lead).
type BusinessFoundDetail struct {
	BusinessID string `json:"businessId"`
	Domain     string `json:"domain"`
	Name       string `json:"name"`
	Vertical   string `json:"vertical,omitempty"`
	Location   string `json:"location,omitempty"`
	Source     string `json:"source"`
	ProfileID  string `json:"profileId"`
}

// businessRow is the DynamoDB shape. Lowercase JSON tags are not
// surfaced through the API — they exist so dynamodbav can use the
// same string for both attribute name and JSON name on read paths.
type businessRow struct {
	PK         string            `dynamodbav:"pk"`
	SK         string            `dynamodbav:"sk"`
	Type       string            `dynamodbav:"type"`
	ID         string            `dynamodbav:"id"`
	Name       string            `dynamodbav:"name"`
	Domain     string            `dynamodbav:"domain"`
	Vertical   string            `dynamodbav:"vertical,omitempty"`
	Location   string            `dynamodbav:"location,omitempty"`
	Source     string            `dynamodbav:"source"`
	SourceRefs map[string]string `dynamodbav:"sourceRefs,omitempty"`
	Status     string            `dynamodbav:"status"`
	Confidence float64           `dynamodbav:"confidence"`
	CreatedAt  string            `dynamodbav:"createdAt"`
	UpdatedAt  string            `dynamodbav:"updatedAt"`
	Etag       string            `dynamodbav:"etag"`
	GSI1PK     string            `dynamodbav:"gsi1pk"`
	GSI1SK     string            `dynamodbav:"gsi1sk"`
	GSI2PK     string            `dynamodbav:"gsi2pk"`
	GSI2SK     string            `dynamodbav:"gsi2sk"`
	GSI3PK     string            `dynamodbav:"gsi3pk"`
	GSI3SK     string            `dynamodbav:"gsi3sk"`
}

// domainExists Queries gsi3 (DOMAIN#<lowercased>) for any existing
// Business row carrying this domain. One-hit is enough — we
// short-circuit dedup as soon as the first row comes back.
func domainExists(ctx context.Context, domain string) (bool, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return false, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String("gsi3"),
		KeyConditionExpression: aws.String("gsi3pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "DOMAIN#" + domain},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("query gsi3: %w", err)
	}
	return len(out.Items) > 0, nil
}

func writeBusiness(ctx context.Context, row businessRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal business: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(ddb.TableName()),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Race: dedup check passed but another writer beat us.
			// Treat as "already exists" — same outcome as the
			// regular dedup path.
			return nil
		}
		return fmt.Errorf("put business: %w", err)
	}
	return nil
}

// buildDeps wires runDeps from the Lambda environment + AWS SDKs.
// Called once per cold start; tests skip this entirely.
func buildDeps(ctx context.Context) (runDeps, error) {
	publisher, err := events.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("build publisher: %w", err)
	}
	return runDeps{
		ListProfiles: targeting.List,
		Providers:    buildProviders(),
		Publish: func(ctx context.Context, env events.Envelope[BusinessFoundDetail]) error {
			return events.Publish(ctx, publisher, env)
		},
		BudgetUSD: killswitch.CapUSD,
		Now:       time.Now,
		NewBizID:  func() string { return randomHex(16) },
	}, nil
}

// buildProviders constructs the three providers. API keys come from
// env vars (populated from SSM by Terraform), CSV bucket likewise.
// Missing config is logged but not fatal — a provider with no
// credentials just returns "APIKey is required" errors that the
// run loop logs and continues past.
func buildProviders() []discovery.Provider {
	providers := []discovery.Provider{}
	if csvBucket := os.Getenv("CSV_BUCKET"); csvBucket != "" {
		if csvKey := os.Getenv("CSV_KEY"); csvKey != "" {
			providers = append(providers, &csvprov.Provider{
				Bucket: csvBucket,
				Key:    csvKey,
			})
		}
	}
	if ch := os.Getenv("COMPANIES_HOUSE_API_KEY"); ch != "" {
		providers = append(providers, &companieshouse.Provider{APIKey: ch})
	}
	if gp := os.Getenv("GOOGLE_PLACES_API_KEY"); gp != "" {
		providers = append(providers, &googleplaces.Provider{APIKey: gp})
	}
	return providers
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("discover: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
