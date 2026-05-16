package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// fakeDDB only needs to support the idempotency conditional PutItem.
type fakeDDB struct{ seen map[string]bool }

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if in.ConditionExpression != nil && strings.Contains(*in.ConditionExpression, "attribute_not_exists(pk)") {
		pk := in.Item["pk"].(*dtypes.AttributeValueMemberS).Value
		if f.seen == nil {
			f.seen = map[string]bool{}
		}
		if f.seen[pk] {
			return nil, &dtypes.ConditionalCheckFailedException{}
		}
		f.seen[pk] = true
	}
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	ddb.SetClient(&fakeDDB{})
	t.Cleanup(func() { ddb.SetClient(nil) })
}

type captured struct {
	suppressed []string
	suppRows   []SuppressionRow
	events     []EmailEventRow
	published  []struct {
		Name   string
		Detail FeedbackDetail
	}
}

func newDeps(c *captured, idx *msgIndex) runDeps {
	return runDeps{
		Suppress: func(_ context.Context, email, _ string) error {
			c.suppressed = append(c.suppressed, email)
			return nil
		},
		PutSuppression: func(_ context.Context, row SuppressionRow) error {
			c.suppRows = append(c.suppRows, row)
			return nil
		},
		LookupMsg: func(context.Context, string) (*msgIndex, error) { return idx, nil },
		PutEvent: func(_ context.Context, row EmailEventRow) error {
			c.events = append(c.events, row)
			return nil
		},
		Publish: func(_ context.Context, name string, d FeedbackDetail) error {
			c.published = append(c.published, struct {
				Name   string
				Detail FeedbackDetail
			}{name, d})
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) },
	}
}

func sqsRecordOf(t *testing.T, sesJSON string) lambdaevents.SQSMessage {
	t.Helper()
	env, _ := json.Marshal(snsEnvelope{Type: "Notification", Message: sesJSON})
	return lambdaevents.SQSMessage{MessageId: "sqs-1", Body: string(env)}
}

func TestProcess_PermanentBounce_SuppressesAndEvents(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, &msgIndex{BusinessID: "biz-1", DraftID: "draft-1"})
	ses := `{"eventType":"Bounce","mail":{"messageId":"m-1"},"bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"Jane@Acme.CO.uk"}]}}`
	if err := processRecord(context.Background(), d, sqsRecordOf(t, ses)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 1 || c.suppressed[0] != "Jane@Acme.CO.uk" {
		t.Errorf("suppress drift: %v", c.suppressed)
	}
	if len(c.suppRows) != 1 || c.suppRows[0].PK != "SUPPRESSION#jane@acme.co.uk" || c.suppRows[0].Reason != "bounce" || c.suppRows[0].ExpiresAt == 0 {
		t.Errorf("Suppression row drift: %+v", c.suppRows)
	}
	if len(c.events) != 1 || c.events[0].Event != "bounced" || c.events[0].PK != "BUSINESS#biz-1" {
		t.Errorf("EmailEvent drift: %+v", c.events)
	}
	if len(c.published) != 1 || c.published[0].Name != "email.bounced" || c.published[0].Detail.SESMessageID != "m-1" {
		t.Errorf("publish drift: %+v", c.published)
	}
}

func TestProcess_TransientBounce_NoSuppression(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, &msgIndex{BusinessID: "biz-1", DraftID: "draft-1"})
	ses := `{"eventType":"Bounce","mail":{"messageId":"m-2"},"bounce":{"bounceType":"Transient","bouncedRecipients":[{"emailAddress":"j@acme.co.uk"}]}}`
	if err := processRecord(context.Background(), d, sqsRecordOf(t, ses)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 0 || len(c.suppRows) != 0 {
		t.Errorf("transient bounce must NOT suppress: %v / %+v", c.suppressed, c.suppRows)
	}
	if len(c.events) != 1 || c.events[0].Event != "bounced" {
		t.Errorf("transient bounce should still record an EmailEvent: %+v", c.events)
	}
	if len(c.published) != 1 || c.published[0].Name != "email.bounced" {
		t.Errorf("email.bounced should still publish: %+v", c.published)
	}
}

func TestProcess_Complaint_Suppresses(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, &msgIndex{BusinessID: "biz-1", DraftID: "draft-1"})
	ses := `{"eventType":"Complaint","mail":{"messageId":"m-3"},"complaint":{"complainedRecipients":[{"emailAddress":"x@acme.co.uk"}],"complaintFeedbackType":"abuse"}}`
	if err := processRecord(context.Background(), d, sqsRecordOf(t, ses)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 1 || c.suppRows[0].Reason != "complaint" {
		t.Errorf("complaint must suppress: %v / %+v", c.suppressed, c.suppRows)
	}
	if c.published[0].Name != "email.complained" {
		t.Errorf("want email.complained, got %s", c.published[0].Name)
	}
}

func TestProcess_Delivery_NoSuppressionEventOnly(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, &msgIndex{BusinessID: "biz-1", DraftID: "draft-1"})
	ses := `{"eventType":"Delivery","mail":{"messageId":"m-4"},"delivery":{"recipients":["j@acme.co.uk"]}}`
	if err := processRecord(context.Background(), d, sqsRecordOf(t, ses)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 0 {
		t.Error("delivery must not suppress")
	}
	if len(c.events) != 1 || c.events[0].Event != "delivered" {
		t.Errorf("delivery EmailEvent drift: %+v", c.events)
	}
}

func TestProcess_MissingIndex_StillSuppresses(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, nil) // no SESMSG# reverse index (pre-index send)
	ses := `{"eventType":"Bounce","mail":{"messageId":"m-5"},"bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"j@acme.co.uk"}]}}`
	if err := processRecord(context.Background(), d, sqsRecordOf(t, ses)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 1 || len(c.suppRows) != 1 {
		t.Error("suppression must still happen without the reverse index (compliance is email-keyed)")
	}
	if len(c.events) != 0 {
		t.Error("no business attribution ⇒ no EmailEvent")
	}
	if len(c.published) != 1 || c.published[0].Detail.BusinessID != "" {
		t.Errorf("email.bounced should still publish (no businessId): %+v", c.published)
	}
}

func TestProcess_IdempotentReplay(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, &msgIndex{BusinessID: "biz-1"})
	rec := sqsRecordOf(t, `{"eventType":"Bounce","mail":{"messageId":"dup-1"},"bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"j@acme.co.uk"}]}}`)
	if err := processRecord(context.Background(), d, rec); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := processRecord(context.Background(), d, rec); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(c.suppressed) != 1 {
		t.Errorf("replay must be a no-op, suppressed=%v", c.suppressed)
	}
}

func TestProcess_MalformedDropped(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, nil)
	if err := processRecord(context.Background(), d, lambdaevents.SQSMessage{MessageId: "x", Body: "not json"}); err != nil {
		t.Errorf("malformed SNS body must be dropped (nil), got %v", err)
	}
}

// Inbound bounce/complaint processing must NOT be gated on the outreach
// kill switch — there is no kill-switch wrapper to disable. A malformed
// record is dropped (nil) so handle() loops without a real AWS call.
func TestHandle_NotKillSwitchGated_DropsPoison(t *testing.T) {
	setup(t)
	t.Setenv("EVENT_BUS_NAME", "pipeline-test")
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{
		{MessageId: "x", Body: "not json"},
	}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("poison record must be dropped, not retried: %v", resp.BatchItemFailures)
	}
}
