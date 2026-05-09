// Package killswitch reads the operator-controlled PipelineSettings record
// and exposes the master kill switch + per-stage flags + per-stage budget
// caps. Per .ralph/specs/05-capacity-and-cost.md, every consumer Lambda
// calls Allowed(ctx, stage) at handler entry and bails out of the work
// (returning success — `pipeline.skipped_killed` metric) when the answer
// is false. Reads are cached 60 seconds in the warm Lambda container so
// a hot path doesn't hammer DynamoDB on every invocation.
//
// PipelineSettings is a singleton row at:
//
//	pk = SETTINGS#PIPELINE
//	sk = CURRENT
//
// Iteration 0.F.1 seeds the row via aws_dynamodb_table_item with
// lifecycle.ignore_changes=[item] so operators can edit it via the
// /settings UI without Terraform reverting their changes.
package killswitch

// PK and SK constants for the singleton PipelineSettings row.
const (
	SettingsPK = "SETTINGS#PIPELINE"
	SettingsSK = "CURRENT"
)

// Settings mirrors the JSON shape from .ralph/specs/05-capacity-and-cost.md.
// Every field is read on every consumer entry; if the spec adds new fields,
// add them here so the cache reflects them.
type Settings struct {
	PipelineEnabled   bool              `json:"pipelineEnabled"   dynamodbav:"pipelineEnabled"`
	Stages            StageFlags        `json:"stages"            dynamodbav:"stages"`
	Caps              Caps              `json:"caps"              dynamodbav:"caps"`
	Thresholds        Thresholds        `json:"thresholds"        dynamodbav:"thresholds"`
	Budgets           Budgets           `json:"budgets"           dynamodbav:"budgets"`
	StagePauseReasons StagePauseReasons `json:"stagePauseReasons" dynamodbav:"stagePauseReasons"`
}

// StagePauseReasons records WHY each disabled stage is disabled, mirroring
// StageFlags one-for-one. Empty string = "not paused, or paused without a
// machine-known reason" (e.g. operator manually toggled it off via
// /settings — those re-enable manually too). The reserved value
// "budget" means the stage was paused by pkg/cost when the daily cap
// was hit; the cost-rollover Lambda (iter 0.F.4) re-enables those at
// 00:05 UTC daily.
//
// Operator-flipped pauses do not get a reason set — keeping them paused
// across rollover is the right behaviour. Only "budget" pauses are
// transient.
type StagePauseReasons struct {
	Discovery string `json:"discovery,omitempty" dynamodbav:"discovery,omitempty"`
	Audit     string `json:"audit,omitempty"     dynamodbav:"audit,omitempty"`
	Preview   string `json:"preview,omitempty"   dynamodbav:"preview,omitempty"`
	Outreach  string `json:"outreach,omitempty"  dynamodbav:"outreach,omitempty"`
}

// PauseReasonBudget is the reserved value the cost-cap mechanism writes
// into StagePauseReasons. The rollover Lambda matches against this
// constant, so a typo in either place would break the rollover loop.
const PauseReasonBudget = "budget"

// StageFlags are the four operator-facing toggles. Internal stages roll up
// to one of these — see the StageMap docstring.
type StageFlags struct {
	DiscoveryEnabled bool `json:"discoveryEnabled" dynamodbav:"discoveryEnabled"`
	AuditEnabled     bool `json:"auditEnabled"     dynamodbav:"auditEnabled"`
	PreviewEnabled   bool `json:"previewEnabled"   dynamodbav:"previewEnabled"`
	OutreachEnabled  bool `json:"outreachEnabled"  dynamodbav:"outreachEnabled"`
}

// Caps controls per-day call counts.
type Caps struct {
	MaxDiscoveriesPerDay int `json:"maxDiscoveriesPerDay" dynamodbav:"maxDiscoveriesPerDay"`
	MaxAuditsPerDay      int `json:"maxAuditsPerDay"      dynamodbav:"maxAuditsPerDay"`
	MaxPreviewsPerDay    int `json:"maxPreviewsPerDay"    dynamodbav:"maxPreviewsPerDay"`
	MaxEmailsPerDay      int `json:"maxEmailsPerDay"      dynamodbav:"maxEmailsPerDay"`
	MaxReviewQueueSize   int `json:"maxReviewQueueSize"   dynamodbav:"maxReviewQueueSize"`
	MaxBacklogSize       int `json:"maxBacklogSize"       dynamodbav:"maxBacklogSize"`
}

// Thresholds gate the qualifier + spec-generator pipelines.
type Thresholds struct {
	MinTechnicalIssueScore int     `json:"minTechnicalIssueScore" dynamodbav:"minTechnicalIssueScore"`
	MinQualificationScore  int     `json:"minQualificationScore"  dynamodbav:"minQualificationScore"`
	MinContactConfidence   float64 `json:"minContactConfidence"   dynamodbav:"minContactConfidence"`
}

// Budgets are the per-stage daily-spend caps in USD passed to
// pkg/cost.WithCostCap. CapUSD(ctx, stage) maps a stage name to the right
// field.
type Budgets struct {
	DailyBedrockUsd float64 `json:"dailyBedrockUsd" dynamodbav:"dailyBedrockUsd"`
	DailyPlacesUsd  float64 `json:"dailyPlacesUsd"  dynamodbav:"dailyPlacesUsd"`
	DailyEmailUsd   float64 `json:"dailyEmailUsd"   dynamodbav:"dailyEmailUsd"`
}

// Defaults returns the project's documented default Settings for testing
// and for the iter 0.F.1 seed. Values come straight from
// .ralph/specs/05-capacity-and-cost.md § "The Pipeline Settings record".
func Defaults() Settings {
	return Settings{
		PipelineEnabled: true,
		Stages: StageFlags{
			DiscoveryEnabled: true,
			AuditEnabled:     true,
			PreviewEnabled:   true,
			OutreachEnabled:  false,
		},
		Caps: Caps{
			MaxDiscoveriesPerDay: 100,
			MaxAuditsPerDay:      50,
			MaxPreviewsPerDay:    10,
			MaxEmailsPerDay:      5,
			MaxReviewQueueSize:   20,
			MaxBacklogSize:       500,
		},
		Thresholds: Thresholds{
			MinTechnicalIssueScore: 30,
			MinQualificationScore:  70,
			MinContactConfidence:   0.6,
		},
		Budgets: Budgets{
			DailyBedrockUsd: 5,
			DailyPlacesUsd:  2,
			DailyEmailUsd:   1,
		},
	}
}
