package killswitch

import (
	"context"
	"fmt"
	"log/slog"
)

// WithKillSwitch is the standard entry-guard for every consumer Lambda.
// It folds the three required steps into one call:
//
//  1. Look up the operator's pipeline + per-stage flags (cached 60s in
//     warm container — see Get).
//  2. If the stage is disabled, emit a structured `pipeline.<stage>.skipped_killed`
//     log line that CloudWatch metric filters convert to a metric, then
//     return nil. The Lambda runtime treats nil as a successful event
//     consumption — no DLQ, no retry, no work.
//  3. Otherwise, invoke fn(ctx) and surface its error verbatim.
//
// On Get error the wrapper fails closed: it surfaces the error so the
// Lambda retries via its DLQ. A transient DynamoDB hiccup must not
// silently bypass the kill switch.
//
// The unknown-stage case (passing a stage outside `StageMap`'s values)
// also returns an error — better to fail loud in CI tests than to ship
// a consumer that pretends to be gated.
//
// Usage at consumer entry, e.g. lambdas/audit/main.go (iter 2.3):
//
//	func handle(ctx context.Context, evt events.SQSEvent) error {
//	    return killswitch.WithKillSwitch(ctx, killswitch.StageAudit, func(ctx context.Context) error {
//	        // … real work …
//	    })
//	}
func WithKillSwitch(ctx context.Context, stage string, fn func(context.Context) error) error {
	allowed, err := Allowed(ctx, stage)
	if err != nil {
		return fmt.Errorf("killswitch: pre-flight check for stage %q: %w", stage, err)
	}
	if !allowed {
		logSkipped(ctx, stage)
		return nil
	}
	return fn(ctx)
}

// logSkipped emits the structured line that CloudWatch metric filters
// convert into the `pipeline.<stage>.skipped_killed` metric. The shape
// is fixed — changing it without updating the metric filter would
// silently break the alerting and dashboards in 08-admin-ui.md.
func logSkipped(ctx context.Context, stage string) {
	logger := loggerFromContext(ctx)
	logger.InfoContext(ctx, "pipeline.skipped_killed",
		"stage", stage,
		"metric", "pipeline."+stage+".skipped_killed",
	)
}

// loggerFromContext is a tiny indirection so tests can inject a recording
// logger without depending on pkg/log (which would create a cycle when
// pkg/log eventually depends on pkg/killswitch for log-level checks).
var loggerFromContext = func(_ context.Context) *slog.Logger {
	return slog.Default()
}
