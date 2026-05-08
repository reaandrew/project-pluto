package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestHandleHealth(t *testing.T) {
	t.Setenv("ENVIRONMENT", "unit-test")
	t.Setenv("ITEMS_TABLE", "website-agency-items-unit-test")

	resp, err := handle(context.Background(), events.APIGatewayV2HTTPRequest{})
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Headers["Content-Type"] != "application/json" {
		t.Errorf("expected JSON content type, got %q", resp.Headers["Content-Type"])
	}

	var body response
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Env != "unit-test" {
		t.Errorf("expected env=unit-test, got %q", body.Env)
	}
	if body.Message != "hello from website-agency" {
		t.Errorf("expected greeting, got %q", body.Message)
	}
	if body.ItemsTable != "website-agency-items-unit-test" {
		t.Errorf("expected ItemsTable to come from env, got %q", body.ItemsTable)
	}
	if body.Ts == 0 {
		t.Error("expected non-zero timestamp")
	}
}
