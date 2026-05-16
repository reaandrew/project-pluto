// tuner-style (iter 9.3): weekly EventBridge-scheduled Lambda that
// proposes VerticalStyleGuide deltas from operator spec/website
// feedback. Thin wrapper over pkg/tunerlib (the shared tuner core);
// not kill-switch gated; the Sonnet call is cost-capped from the
// Bedrock budget; proposals are PENDING-only (operator applies in 9.4).
package main

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tunerlib"
)

const consumer = "tuner-style"

func verticals() []string {
	if v := os.Getenv("TUNER_VERTICALS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"default", "accountants"} // the seeded style guides
}

func handle(ctx context.Context) error {
	deps, err := tunerlib.BuildDeps(ctx, consumer, func(vertical string) (string, string, bool) {
		return "STYLE#" + vertical, "PROFILE", true
	})
	if err != nil {
		return err
	}
	return tunerlib.Run(ctx, deps, tunerlib.Config{
		Consumer:  consumer,
		Kind:      "style",
		PromptID:  prompts.TunerStyleV1.ID,
		Verticals: verticals(),
		Subjects:  []string{"spec", "website"},
		SinceDays: 7,
		Propose: tunerlib.PromptProposer(prompts.TunerStyleV1,
			func(t schemas.TunerStyleV1) string { return t.Rationale }),
	})
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
