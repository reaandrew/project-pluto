// tuner-targeting (iter 9.3): weekly Lambda proposing TargetingProfile
// deltas from operator discovery/qualification feedback. Thin wrapper
// over pkg/tunerlib. TargetingProfile has no per-vertical key
// (TARGET#<id>), so no currentProfile is loaded — the model proposes
// from the rejection/approval signal alone (documented MVP scope; the
// operator sees the delta against the live profile in /tuners, 9.4).
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

const consumer = "tuner-targeting"

func verticals() []string {
	if v := os.Getenv("TUNER_VERTICALS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"default", "accountants"}
}

func handle(ctx context.Context) error {
	deps, err := tunerlib.BuildDeps(ctx, consumer, func(string) (string, string, bool) {
		return "", "", false // no per-vertical targeting profile item
	})
	if err != nil {
		return err
	}
	return tunerlib.Run(ctx, deps, tunerlib.Config{
		Consumer:  consumer,
		Kind:      "targeting",
		PromptID:  prompts.TunerTargetingV1.ID,
		Verticals: verticals(),
		Subjects:  []string{"audit", "qualification"},
		SinceDays: 7,
		Propose: tunerlib.PromptProposer(prompts.TunerTargetingV1,
			func(t schemas.TunerTargetingV1) string { return t.Rationale }),
	})
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
