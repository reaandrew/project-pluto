package killswitch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// CacheTTL is how long a fetched Settings stays in process before we
// re-read DynamoDB. 60s is the spec's value (05-capacity-and-cost.md
// line 42: "Read on every consumer entry (cached 60 seconds in Lambda
// warm container)") — striking the balance between operator-toggle
// latency and DDB read load.
var (
	cacheMu  sync.RWMutex
	cacheTTL = 60 * time.Second
	cached   *Settings
	cachedAt time.Time
)

// nowFunc is overridable in tests for deterministic cache-expiry checks.
var nowFunc = func() time.Time { return time.Now().UTC() }

// Get returns the current PipelineSettings, refreshing from DynamoDB when
// the in-process cache has expired (CacheTTL since last fetch). Concurrent
// callers during a refresh may see one extra DDB read but never stale data
// past the TTL.
func Get(ctx context.Context) (Settings, error) {
	cacheMu.RLock()
	if cached != nil && nowFunc().Sub(cachedAt) < cacheTTL {
		s := *cached
		cacheMu.RUnlock()
		return s, nil
	}
	cacheMu.RUnlock()

	fresh, err := fetch(ctx)
	if err != nil {
		return Settings{}, err
	}
	cacheMu.Lock()
	cached = &fresh
	cachedAt = nowFunc()
	cacheMu.Unlock()
	return fresh, nil
}

// Allowed reports whether the named operator-facing stage may run right
// now. Returns false when either the master kill switch (pipelineEnabled)
// is off OR the per-stage flag is off. Returns an error for unknown stage
// strings — callers should map their consumer-Lambda name to one of the
// four operator stages themselves (see StageMap).
//
// A consumer Lambda that gets `(false, nil)` should record the
// `pipeline.<stage>.skipped_killed` metric and return success without
// doing the work — the Lambda runtime treats a no-op return as a
// successful event consumption (no DLQ, no retry).
func Allowed(ctx context.Context, stage string) (bool, error) {
	s, err := Get(ctx)
	if err != nil {
		return false, err
	}
	if !s.PipelineEnabled {
		return false, nil
	}
	switch stage {
	case StageDiscovery:
		return s.Stages.DiscoveryEnabled, nil
	case StageAudit:
		return s.Stages.AuditEnabled, nil
	case StagePreview:
		return s.Stages.PreviewEnabled, nil
	case StageOutreach:
		return s.Stages.OutreachEnabled, nil
	default:
		return false, fmt.Errorf("killswitch: unknown stage %q (want one of %s)", stage, knownStages())
	}
}

// CapUSD returns the per-stage budget cap. Maps the operator-facing stage
// name + the bedrock-stage names from pkg/bedrock to the right Budgets
// field:
//
//	"discovery" → 0  (no Bedrock)
//	"audit"     → DailyBedrockUsd
//	"preview"   → DailyBedrockUsd  (covers the spec-generator + generator + publisher chain)
//	"outreach"  → DailyEmailUsd
//	"places"    → DailyPlacesUsd
//
// Pass these to cost.Assert / cost.WithCostCap. CapUSD returns 0 (no cap)
// for stages that aren't budget-capped.
func CapUSD(ctx context.Context, stage string) (float64, error) {
	s, err := Get(ctx)
	if err != nil {
		return 0, err
	}
	switch stage {
	case StageAudit, StagePreview:
		return s.Budgets.DailyBedrockUsd, nil
	case StageOutreach:
		return s.Budgets.DailyEmailUsd, nil
	case StagePlaces:
		return s.Budgets.DailyPlacesUsd, nil
	case StageDiscovery:
		return 0, nil
	default:
		return 0, fmt.Errorf("killswitch: unknown stage %q (want one of %s)", stage, knownStages())
	}
}

// SetSettings overrides the in-process cache. Intended for tests and for
// startup cold-paths that load settings out-of-band. Pass nil to clear.
func SetSettings(s *Settings) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if s == nil {
		cached = nil
		cachedAt = time.Time{}
		return
	}
	copy := *s
	cached = &copy
	cachedAt = nowFunc()
}

// SetCacheTTL overrides the cache TTL. Intended for tests.
func SetCacheTTL(d time.Duration) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheTTL = d
}

// fetch reads the singleton row from DynamoDB. A missing row is treated
// as an explicit error — production must seed the row at deploy time
// (iter 0.F.1).
func fetch(ctx context.Context) (Settings, error) {
	var s Settings
	client, err := ddb.Client(ctx)
	if err != nil {
		return s, fmt.Errorf("killswitch: getting DynamoDB client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return s, errors.New("killswitch: ITEMS_TABLE is not set")
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: SettingsPK},
			"sk": &dtypes.AttributeValueMemberS{Value: SettingsSK},
		},
		// Strong-consistent read so an operator toggling a flag sees the
		// change reflected within CacheTTL on every Lambda — eventual
		// reads could leave one container 60s stale plus the eventual
		// replication lag.
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return s, fmt.Errorf("killswitch: GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return s, fmt.Errorf("killswitch: PipelineSettings row not found at %s/%s — has 0.F.1 seed run?", SettingsPK, SettingsSK)
	}
	if err := attributevalue.UnmarshalMap(out.Item, &s); err != nil {
		return s, fmt.Errorf("killswitch: unmarshalling settings: %w", err)
	}
	return s, nil
}
