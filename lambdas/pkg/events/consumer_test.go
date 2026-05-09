package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestUnmarshalRoundTrip(t *testing.T) {
	env := New("business.found", "discovery", sampleDetail{BusinessID: "b-1", Domain: "acme.test"})
	raw := mustJSON(t, env)

	got, err := Unmarshal[sampleDetail](raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.EventID != env.EventID || got.Detail.BusinessID != "b-1" {
		t.Errorf("envelope drift: %+v", got)
	}
}

func TestUnmarshalRejectsInvalid(t *testing.T) {
	bad := mustJSON(t, Envelope[sampleDetail]{}) // empty
	if _, err := Unmarshal[sampleDetail](bad); err == nil {
		t.Fatal("expected validation error on empty envelope")
	}

	if _, err := Unmarshal[sampleDetail]([]byte("not json")); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestFromEventBridge(t *testing.T) {
	env := New("business.found", "discovery", sampleDetail{BusinessID: "b-1"})
	ebEvent := events.EventBridgeEvent{
		Source:     Source,
		DetailType: env.EventName,
		Detail:     mustJSON(t, env),
	}
	got, err := FromEventBridge[sampleDetail](ebEvent)
	if err != nil {
		t.Fatalf("FromEventBridge: %v", err)
	}
	if got.Detail.BusinessID != "b-1" {
		t.Errorf("detail drift: %+v", got)
	}
}

func TestFromSQS(t *testing.T) {
	env := New("business.found", "discovery", sampleDetail{BusinessID: "b-1"})
	ebEvent := events.EventBridgeEvent{
		Source:     Source,
		DetailType: env.EventName,
		Detail:     mustJSON(t, env),
	}
	msg := events.SQSMessage{
		MessageId: "m-1",
		Body:      string(mustJSON(t, ebEvent)),
	}
	got, err := FromSQS[sampleDetail](msg)
	if err != nil {
		t.Fatalf("FromSQS: %v", err)
	}
	if got.Detail.BusinessID != "b-1" {
		t.Errorf("detail drift: %+v", got)
	}
}

func TestFromSQSRejectsBadBody(t *testing.T) {
	if _, err := FromSQS[sampleDetail](events.SQSMessage{Body: "not-json"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestConsumePartialBatchFailure(t *testing.T) {
	envOk := New("x", "svc", sampleDetail{BusinessID: "ok"})
	envFail := New("x", "svc", sampleDetail{BusinessID: "fail"})

	wrap := func(env Envelope[sampleDetail]) string {
		return string(mustJSON(t, events.EventBridgeEvent{
			Source: Source, DetailType: env.EventName, Detail: mustJSON(t, env),
		}))
	}

	batch := events.SQSEvent{Records: []events.SQSMessage{
		{MessageId: "m-good", Body: wrap(envOk)},
		{MessageId: "m-bad", Body: wrap(envFail)},
		{MessageId: "m-broken", Body: "not-json"},
	}}

	processed := []string{}
	resp, err := Consume(context.Background(), batch, func(_ context.Context, e Envelope[sampleDetail]) error {
		processed = append(processed, e.Detail.BusinessID)
		if e.Detail.BusinessID == "fail" {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(processed) != 2 || processed[0] != "ok" || processed[1] != "fail" {
		t.Errorf("expected handler called for ok and fail, got %v", processed)
	}

	failedIDs := map[string]bool{}
	for _, f := range resp.BatchItemFailures {
		failedIDs[f.ItemIdentifier] = true
	}
	if !failedIDs["m-bad"] || !failedIDs["m-broken"] {
		t.Errorf("expected failures for m-bad and m-broken, got %v", resp.BatchItemFailures)
	}
	if failedIDs["m-good"] {
		t.Errorf("good record should not be in failure list")
	}
}

func TestConsumeAllSucceedNoFailures(t *testing.T) {
	env := New("x", "svc", sampleDetail{BusinessID: "ok"})
	body := mustJSON(t, events.EventBridgeEvent{
		Source: Source, DetailType: env.EventName, Detail: mustJSON(t, env),
	})
	batch := events.SQSEvent{Records: []events.SQSMessage{
		{MessageId: "m-1", Body: string(body)},
	}}

	resp, err := Consume(context.Background(), batch, func(_ context.Context, _ Envelope[sampleDetail]) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("expected no failures, got %v", resp.BatchItemFailures)
	}
}
