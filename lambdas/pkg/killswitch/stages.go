package killswitch

import "strings"

// Operator-facing stage names — these are the four toggles in the /settings
// UI and the four flags on PipelineSettings.Stages.
const (
	StageDiscovery = "discovery"
	StageAudit     = "audit"
	StagePreview   = "preview"
	StageOutreach  = "outreach"
)

// StagePlaces is the synthetic budget-only stage for Google Places API
// spend (no kill-switch flag — Places is gated by Discovery's flag).
const StagePlaces = "places"

// StageMap captures the documented rollup from internal consumer-Lambda
// names to operator-facing stages. Consumer Lambdas should look up their
// own stage at startup and pass it to Allowed/CapUSD:
//
//	stage := killswitch.StageFor("publisher")  // returns "preview"
//	if ok, _ := killswitch.Allowed(ctx, stage); !ok { return nil }
var StageMap = map[string]string{
	"discover":       StageDiscovery,
	"audit":          StageAudit,
	"qualifier":      StageAudit,
	"spec-generator": StagePreview,
	"generator":      StagePreview,
	"publisher":      StagePreview,
	"screenshotter":  StagePreview,
	"email-draft":    StageOutreach,
	"sender":         StageOutreach,

	// Tuners run on a separate weekly schedule and aren't gated by the
	// per-stage flags. Map them to "" so StageFor returns "" — callers
	// should treat empty as "no kill-switch check needed".
	"tuner-style":      "",
	"tuner-email-tone": "",
	"tuner-targeting":  "",
}

// StageFor returns the operator-facing stage name for a consumer-Lambda
// name. Returns "" if the consumer is not gated by a stage flag (e.g.
// tuners), or if the name is unknown — callers should treat unknown the
// same way as ungated, surfacing the issue elsewhere (e.g. in tests).
func StageFor(consumer string) string {
	return StageMap[consumer]
}

// knownStages is used in error messages for Allowed/CapUSD when an
// unknown stage is passed.
func knownStages() string {
	return strings.Join([]string{StageDiscovery, StageAudit, StagePreview, StageOutreach, StagePlaces}, ", ")
}
