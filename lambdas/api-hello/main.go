package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

type response struct {
	Message    string `json:"message"`
	Env        string `json:"env"`
	Ts         int64  `json:"ts"`
	ItemsTable string `json:"items_table"`
	GitSHA     string `json:"git_sha,omitempty"`
}

// loadConfig is called at the start of main() — fails fast in production if env
// is misconfigured. Tests can set env vars and call handle() directly without
// hitting this path.
func handle(ctx context.Context, _ events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	cfg := config.MustLoad()
	logger := applog.FromContext(ctx)
	logger.Info("health check", "env", cfg.Environment)

	body, _ := json.Marshal(response{
		Message:    "hello from ai-website-agency",
		Env:        cfg.Environment,
		Ts:         time.Now().Unix(),
		ItemsTable: cfg.ItemsTable,
		GitSHA:     os.Getenv("GIT_SHA"),
	})

	return httpresp.JSON(200, string(body)), nil
}

func main() {
	// Fail-fast config check at cold start — better than a nil panic on first request.
	_ = config.MustLoad()
	lambda.Start(handle)
}
