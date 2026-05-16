// tuner-email-tone (iter 9.3): weekly Lambda proposing
// EmailToneProfile deltas from operator email feedback. Thin wrapper
// over pkg/tunerlib.
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

const consumer = "tuner-email-tone"

func verticals() []string {
	if v := os.Getenv("TUNER_VERTICALS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"default", "accountants"}
}

func handle(ctx context.Context) error {
	deps, err := tunerlib.BuildDeps(ctx, consumer, func(vertical string) (string, string, bool) {
		return "EMAIL_TONE#" + vertical, "PROFILE", true
	})
	if err != nil {
		return err
	}
	return tunerlib.Run(ctx, deps, tunerlib.Config{
		Consumer:  consumer,
		Kind:      "email_tone",
		PromptID:  prompts.TunerEmailToneV1.ID,
		Verticals: verticals(),
		Subjects:  []string{"email"},
		SinceDays: 7,
		Propose: tunerlib.PromptProposer(prompts.TunerEmailToneV1,
			func(t schemas.TunerEmailToneV1) string { return t.Rationale }),
	})
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
