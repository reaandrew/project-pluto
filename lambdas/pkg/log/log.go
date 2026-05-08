// Package log wraps slog with a JSON handler tuned for CloudWatch Logs (each line
// becomes a structured JSON event). Lambdas call FromContext(ctx) to get a logger
// pre-populated with the AWS request id when one is available.
package log

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

var defaultLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level: parseLevel(os.Getenv("LOG_LEVEL")),
}))

func parseLevel(s string) slog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// FromContext returns the default logger augmented with the AWS request id when
// available. Cheap — no allocation when no context is present.
func FromContext(ctx context.Context) *slog.Logger {
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		return defaultLogger.With("request_id", lc.AwsRequestID)
	}
	return defaultLogger
}
