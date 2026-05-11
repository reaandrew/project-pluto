// Package main is the api-targeting Lambda. CRUD on TargetingProfile rows:
//
//	GET    /targeting       → list (paginated by Scan today; revisit if >100 profiles)
//	GET    /targeting/{id}  → fetch one
//	POST   /targeting       → create (id + etag + createdAt generated server-side)
//	PATCH  /targeting/{id}  → update (requires If-Match header carrying the
//	                          last-seen etag for optimistic concurrency)
//	DELETE /targeting/{id}  → delete
//
// All routes are operator-only — same Cognito JWT authorizer +
// in-handler operator-group check the api-settings Lambda (iter 0.F.2)
// uses.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}

	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	id := req.PathParameters["id"]

	switch {
	case method == "GET" && id == "":
		return handleList(ctx, logger)
	case method == "GET" && id != "":
		return handleGet(ctx, logger, id)
	case method == "POST" && id == "":
		return handleCreate(ctx, logger, req.Body)
	case method == "PATCH" && id != "":
		return handleUpdate(ctx, logger, id, req.Body, req.Headers)
	case method == "DELETE" && id != "":
		return handleDelete(ctx, logger, id)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

func handleList(ctx context.Context, logger applogger) (events.APIGatewayV2HTTPResponse, error) {
	out, err := targeting.List(ctx)
	if err != nil {
		logger.Error("targeting.list failed", "err", err)
		return httpresp.Error(500, "could not list profiles"), nil
	}
	// Wrap in an envelope so future paging tokens have a home.
	body, err := json.Marshal(map[string]any{"profiles": out})
	if err != nil {
		logger.Error("targeting.list marshal failed", "err", err)
		return httpresp.Error(500, "could not encode profiles"), nil
	}
	return httpresp.JSON(200, string(body)), nil
}

func handleGet(ctx context.Context, logger applogger, id string) (events.APIGatewayV2HTTPResponse, error) {
	p, err := targeting.Get(ctx, id)
	if errors.Is(err, targeting.ErrNotFound) {
		return httpresp.Error(404, "profile not found"), nil
	}
	if errors.Is(err, targeting.ErrInvalid) {
		return httpresp.Error(400, err.Error()), nil
	}
	if err != nil {
		logger.Error("targeting.get failed", "err", err, "id", id)
		return httpresp.Error(500, "could not read profile"), nil
	}
	body, _ := json.Marshal(p)
	return httpresp.JSON(200, string(body)), nil
}

func handleCreate(ctx context.Context, logger applogger, body string) (events.APIGatewayV2HTTPResponse, error) {
	if strings.TrimSpace(body) == "" {
		return httpresp.Error(400, "empty body"), nil
	}
	var p targeting.Profile
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return httpresp.Error(400, "invalid JSON: "+err.Error()), nil
	}
	out, err := targeting.Create(ctx, p)
	if errors.Is(err, targeting.ErrInvalid) {
		return httpresp.Error(400, err.Error()), nil
	}
	if err != nil {
		logger.Error("targeting.create failed", "err", err)
		return httpresp.Error(500, "could not create profile"), nil
	}
	resp, _ := json.Marshal(out)
	return httpresp.JSON(201, string(resp)), nil
}

func handleUpdate(ctx context.Context, logger applogger, id, body string, headers map[string]string) (events.APIGatewayV2HTTPResponse, error) {
	if strings.TrimSpace(body) == "" {
		return httpresp.Error(400, "empty body"), nil
	}
	etag := matchEtag(headers)
	if etag == "" {
		return httpresp.Error(428, "If-Match header is required"), nil
	}
	var p targeting.Profile
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return httpresp.Error(400, "invalid JSON: "+err.Error()), nil
	}
	out, err := targeting.Update(ctx, id, p, etag)
	if errors.Is(err, targeting.ErrNotFound) {
		return httpresp.Error(404, "profile not found"), nil
	}
	if errors.Is(err, targeting.ErrEtagMismatch) {
		return httpresp.Error(412, "etag mismatch — refresh and retry"), nil
	}
	if errors.Is(err, targeting.ErrInvalid) {
		return httpresp.Error(400, err.Error()), nil
	}
	if err != nil {
		logger.Error("targeting.update failed", "err", err, "id", id)
		return httpresp.Error(500, "could not update profile"), nil
	}
	resp, _ := json.Marshal(out)
	return httpresp.JSON(200, string(resp)), nil
}

func handleDelete(ctx context.Context, logger applogger, id string) (events.APIGatewayV2HTTPResponse, error) {
	if err := targeting.Delete(ctx, id); err != nil {
		if errors.Is(err, targeting.ErrNotFound) {
			return httpresp.Error(404, "profile not found"), nil
		}
		logger.Error("targeting.delete failed", "err", err, "id", id)
		return httpresp.Error(500, "could not delete profile"), nil
	}
	return httpresp.JSON(204, ""), nil
}

// matchEtag returns the If-Match header value, looking up case-
// insensitively since API Gateway sometimes lower-cases. Returns
// the empty string when missing.
func matchEtag(headers map[string]string) string {
	for k, v := range headers {
		if strings.EqualFold(k, "if-match") {
			// Strip surrounding quotes if present; servers often
			// quote etags ("abc123") but our value is the raw hex.
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// applogger lets the handler take any *slog.Logger (production) or
// a *slog.Logger built around discard (tests) without an extra
// import. Aliased so the file reads cleanly.
type applogger = interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
