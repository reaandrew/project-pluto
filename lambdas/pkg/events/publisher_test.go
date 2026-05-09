package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

type fakeEB struct {
	gotInput  *eventbridge.PutEventsInput
	out       *eventbridge.PutEventsOutput
	returnErr error
}

func (f *fakeEB) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.gotInput = in
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	if f.out != nil {
		return f.out, nil
	}
	return &eventbridge.PutEventsOutput{FailedEntryCount: 0}, nil
}

func TestPublishMarshalsEnvelopeAndSetsEntryFields(t *testing.T) {
	fake := &fakeEB{}
	pub := NewPublisherWithClient(fake, "pipeline-test")

	env := New("business.found", "discovery", sampleDetail{BusinessID: "b-1", Domain: "acme.test"})
	if err := Publish(context.Background(), pub, env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if fake.gotInput == nil || len(fake.gotInput.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", fake.gotInput)
	}
	entry := fake.gotInput.Entries[0]
	if aws.ToString(entry.EventBusName) != "pipeline-test" {
		t.Errorf("bus = %q", aws.ToString(entry.EventBusName))
	}
	if aws.ToString(entry.Source) != Source {
		t.Errorf("source = %q", aws.ToString(entry.Source))
	}
	if aws.ToString(entry.DetailType) != "business.found" {
		t.Errorf("detailType = %q", aws.ToString(entry.DetailType))
	}

	var sentBody Envelope[sampleDetail]
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &sentBody); err != nil {
		t.Fatalf("entry detail not valid JSON: %v", err)
	}
	if sentBody.EventID != env.EventID || sentBody.Detail.BusinessID != "b-1" {
		t.Errorf("detail JSON drift: %+v", sentBody)
	}
}

func TestPublishRejectsInvalidEnvelope(t *testing.T) {
	fake := &fakeEB{}
	pub := NewPublisherWithClient(fake, "pipeline-test")

	bad := Envelope[sampleDetail]{} // all fields empty
	err := Publish(context.Background(), pub, bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if fake.gotInput != nil {
		t.Errorf("client should not have been called for an invalid envelope")
	}
}

func TestPublishSurfacesPartialFailure(t *testing.T) {
	fake := &fakeEB{
		out: &eventbridge.PutEventsOutput{
			FailedEntryCount: 1,
			Entries: []ebtypes.PutEventsResultEntry{{
				ErrorCode:    aws.String("InternalException"),
				ErrorMessage: aws.String("transient"),
			}},
		},
	}
	pub := NewPublisherWithClient(fake, "pipeline-test")

	env := New("x", "svc", sampleDetail{})
	err := Publish(context.Background(), pub, env)
	if err == nil {
		t.Fatal("expected entry-rejected error")
	}
}

func TestPublishWrapsSDKError(t *testing.T) {
	wantErr := errors.New("network down")
	fake := &fakeEB{returnErr: wantErr}
	pub := NewPublisherWithClient(fake, "pipeline-test")

	env := New("x", "svc", sampleDetail{})
	err := Publish(context.Background(), pub, env)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected SDK error to be wrapped, got %v", err)
	}
}

func TestNewPublisherRequiresBusEnvVar(t *testing.T) {
	t.Setenv("EVENT_BUS_NAME", "")
	if _, err := NewPublisher(context.Background()); err == nil {
		t.Fatal("expected error when EVENT_BUS_NAME unset")
	}
}
