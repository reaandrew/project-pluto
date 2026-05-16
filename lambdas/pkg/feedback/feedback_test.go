package feedback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
)

// --- DDB fake ----------------------------------------------------------

type fakeDDB struct {
	puts   []*dynamodb.PutItemInput
	putErr error
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.puts = append(f.puts, in)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

// --- EventBridge fake --------------------------------------------------

type fakeEB struct {
	puts []*eventbridge.PutEventsInput
	err  error
}

func (f *fakeEB) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.puts = append(f.puts, in)
	if f.err != nil {
		return nil, f.err
	}
	return &eventbridge.PutEventsOutput{}, nil
}

// --- harness -----------------------------------------------------------

func setupHarness(t *testing.T) (*fakeDDB, *pkgevents.Publisher, *fakeEB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	d := &fakeDDB{}
	ddb.SetClient(d)
	SetNowFunc(func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) })
	SetIDFunc(func() string { return "feedback-1" })
	eb := &fakeEB{}
	pub := pkgevents.NewPublisherWithClient(eb, "test-bus")
	t.Cleanup(func() {
		ddb.SetClient(nil)
		SetNowFunc(func() time.Time { return time.Now().UTC() })
		SetIDFunc(defaultRandomHex16)
	})
	return d, pub, eb
}

func defaultRandomHex16() string { return defaultRandomHex(16) }

func validInput() CaptureInput {
	return CaptureInput{
		Subject:    SubjectSpec,
		SubjectID:  "spec-1",
		BusinessID: "biz-1",
		Actor:      "cog-abc",
		Action:     ActionEdit,
		Vertical:   "accountants",
		Notes:      "removed 'industry-leading'; added 'fixed-fee accounting'",
	}
}

// TestCapture_ProfileSubject_AllowsEmptyBusinessID covers the iter-9.4
// contract relaxation: a profile-tuning audit row is vertical-scoped,
// so SubjectProfile + ActionApply + empty BusinessID must validate and
// persist (every other subject still requires a businessId — see
// TestCapture_RejectsInvalid).
func TestCapture_ProfileSubject_AllowsEmptyBusinessID(t *testing.T) {
	d, pub, _ := setupHarness(t)
	in := CaptureInput{
		Subject:   SubjectProfile,
		SubjectID: "delta-1",
		Actor:     "cog-abc",
		Action:    ActionApply,
		Vertical:  "accountants",
		Notes:     "applied style tuner delta (v4)",
		// BusinessID intentionally empty.
	}
	row, _, err := Capture(context.Background(), in, pub)
	if err != nil {
		t.Fatalf("profile-subject feedback must validate without businessId: %v", err)
	}
	if row.Subject != SubjectProfile || row.Action != ActionApply || row.BusinessID != "" {
		t.Errorf("row drift: %+v", row)
	}
	if row.PK != "FEEDBACK#accountants" {
		t.Errorf("profile feedback must land in the vertical partition, got %q", row.PK)
	}
	if len(d.puts) != 1 {
		t.Errorf("expected the audit row to be persisted, got %d puts", len(d.puts))
	}
}

// --- happy path --------------------------------------------------------

func TestCapture_PersistsRowAndPublishesEvent(t *testing.T) {
	d, pub, eb := setupHarness(t)
	row, env, err := Capture(context.Background(), validInput(), pub)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if row.ID != "feedback-1" || row.Subject != SubjectSpec || row.Action != ActionEdit {
		t.Errorf("row drift: %+v", row)
	}
	if row.PK != "FEEDBACK#accountants" || !strings.HasSuffix(row.SK, "#feedback-1") {
		t.Errorf("pk/sk drift: pk=%q sk=%q", row.PK, row.SK)
	}
	if row.GSI2PK != "FEEDBACK#VERTICAL#accountants" || !strings.HasPrefix(row.GSI2SK, "spec#") {
		t.Errorf("gsi2 drift: pk=%q sk=%q", row.GSI2PK, row.GSI2SK)
	}
	if len(d.puts) != 1 {
		t.Errorf("expected 1 DDB Put, got %d", len(d.puts))
	}
	if env.EventName != "feedback.captured" || env.Detail.FeedbackID != "feedback-1" {
		t.Errorf("event drift: %+v", env)
	}
	if len(eb.puts) != 1 {
		t.Errorf("expected 1 PutEvents, got %d", len(eb.puts))
	}
}

// --- vertical fallback --------------------------------------------------

func TestCapture_EmptyVerticalFallsBackToDefault(t *testing.T) {
	_, pub, _ := setupHarness(t)
	in := validInput()
	in.Vertical = ""
	row, env, err := Capture(context.Background(), in, pub)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if row.Vertical != "default" {
		t.Errorf("vertical = %q, want default", row.Vertical)
	}
	if row.PK != "FEEDBACK#default" {
		t.Errorf("pk = %q, want FEEDBACK#default", row.PK)
	}
	if env.Detail.Vertical != "default" {
		t.Errorf("event vertical = %q", env.Detail.Vertical)
	}
}

// --- nil publisher → row written, no event ----------------------------

func TestCapture_NilPublisher_StillPersists(t *testing.T) {
	d, _, _ := setupHarness(t)
	row, _, err := Capture(context.Background(), validInput(), nil)
	if err != nil {
		t.Fatalf("Capture with nil publisher: %v", err)
	}
	if row.ID == "" {
		t.Error("row should be written when publisher is nil")
	}
	if len(d.puts) != 1 {
		t.Errorf("expected 1 DDB Put even without publisher; got %d", len(d.puts))
	}
}

// --- validation matrix ------------------------------------------------

func TestCapture_RejectsInvalid(t *testing.T) {
	_, pub, _ := setupHarness(t)
	cases := map[string]CaptureInput{
		"empty subject":    {SubjectID: "x", BusinessID: "x", Actor: "x", Action: ActionEdit},
		"unknown subject":  {Subject: "carrots", SubjectID: "x", BusinessID: "x", Actor: "x", Action: ActionEdit},
		"empty subjectId":  {Subject: SubjectSpec, BusinessID: "x", Actor: "x", Action: ActionEdit},
		"empty businessId": {Subject: SubjectSpec, SubjectID: "x", Actor: "x", Action: ActionEdit},
		"empty actor":      {Subject: SubjectSpec, SubjectID: "x", BusinessID: "x", Action: ActionEdit},
		"unknown action":   {Subject: SubjectSpec, SubjectID: "x", BusinessID: "x", Actor: "x", Action: "delete"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Capture(context.Background(), in, pub)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("expected ErrInvalid for %q, got %v", name, err)
			}
		})
	}
}

// --- ddb error surfaces --------------------------------------------------

func TestCapture_DDBErrorSurfaces(t *testing.T) {
	d, pub, _ := setupHarness(t)
	d.putErr = errors.New("ddb down")
	_, _, err := Capture(context.Background(), validInput(), pub)
	if err == nil {
		t.Fatal("expected ddb error to surface")
	}
}

// --- publish error surfaces; row still persisted ----------------------

func TestCapture_PublishErrorSurfaces_RowStillWritten(t *testing.T) {
	d, pub, eb := setupHarness(t)
	eb.err = errors.New("eventbridge down")
	row, _, err := Capture(context.Background(), validInput(), pub)
	if err == nil {
		t.Fatal("expected publish error to surface")
	}
	if row.ID == "" {
		t.Error("row should be persisted before publish attempt")
	}
	if len(d.puts) != 1 {
		t.Errorf("expected 1 DDB Put on publish failure path; got %d", len(d.puts))
	}
}

// --- DDB item attribute names match the row tags ----------------------

func TestCapture_RowAttributeNames(t *testing.T) {
	d, pub, _ := setupHarness(t)
	if _, _, err := Capture(context.Background(), validInput(), pub); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(d.puts) != 1 {
		t.Fatalf("no DDB Put captured")
	}
	item := d.puts[0].Item
	for _, want := range []string{"pk", "sk", "type", "id", "subject", "subjectId", "businessId", "actor", "action", "vertical", "createdAt", "gsi2pk", "gsi2sk"} {
		if _, ok := item[want]; !ok {
			t.Errorf("DDB item missing attribute %q", want)
		}
	}
	if v, _ := item["type"].(*dtypes.AttributeValueMemberS); v == nil || v.Value != "Feedback" {
		t.Errorf("type attribute drift: %+v", item["type"])
	}
}
