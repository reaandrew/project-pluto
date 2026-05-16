package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/emaildraft"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
)

const testCleartext = "SECRETCODE9"

func newTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeDDB struct {
	idempSeen map[string]bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if in.ConditionExpression != nil && strings.Contains(*in.ConditionExpression, "attribute_not_exists(pk)") {
		if pk, ok := in.Item["pk"].(*dtypes.AttributeValueMemberS); ok && strings.HasPrefix(pk.Value, "IDEMP#") {
			if f.idempSeen == nil {
				f.idempSeen = map[string]bool{}
			}
			if f.idempSeen[pk.Value] {
				return nil, &dtypes.ConditionalCheckFailedException{}
			}
			f.idempSeen[pk.Value] = true
		}
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

func setupItemsTable(t *testing.T) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	ddb.SetClient(&fakeDDB{})
	t.Cleanup(func() { ddb.SetClient(nil) })
}

func setupKillSwitch(t *testing.T, on bool) {
	t.Helper()
	s := killswitch.Defaults()
	s.Stages.OutreachEnabled = on
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

type captured struct {
	putRow    EmailDraftRow
	putCalled bool
	published pkgevents.Envelope[EmailReadyDetail]
	pubCalled bool
}

func draftWithPlaceholder() schemas.EmailV1 {
	return schemas.EmailV1{
		Subject:   "Quick redesign preview for Acme",
		Body:      "Hi Jane,\n\nIssue noted.\nPreview: https://p.example.com/sites/web-1\nUse access code {{PASSCODE}}.\n\nReply 'no thanks'.\n\nAndrew",
		WordCount: 65,
	}
}

func newDeps(t *testing.T, c *captured, web *WebsiteRow) runDeps {
	t.Helper()
	return runDeps{
		GetBusiness: func(context.Context, string) (*BusinessRow, error) {
			return &BusinessRow{ID: "biz-1", Name: "Acme", Domain: "acme.co.uk", Vertical: "accountants"}, nil
		},
		GetWebsite:     func(context.Context, string, string) (*WebsiteRow, error) { return web, nil },
		GetLatestAudit: func(context.Context, string) (*AuditRow, error) { return &AuditRow{ID: "a1", Score: 38}, nil },
		GetLatestContact: func(context.Context, string) (*ContactRow, error) {
			return &ContactRow{ID: "con-1", Name: "Jane Smith", Role: "Director"}, nil
		},
		GetTone: func(context.Context, string) (tone.Profile, error) {
			return tone.Profile{Vertical: "accountants", Version: 1, OptOutLine: "Reply 'no thanks'.", Signature: "Andrew"}, nil
		},
		Decrypt: func(context.Context, string) (string, error) { return testCleartext, nil },
		RunDraft: func(context.Context, emaildraft.Input, float64) (schemas.EmailV1, error) {
			return draftWithPlaceholder(), nil
		},
		PutDraft: func(_ context.Context, row EmailDraftRow) error { c.putRow = row; c.putCalled = true; return nil },
		Publish: func(_ context.Context, env pkgevents.Envelope[EmailReadyDetail]) error {
			c.published = env
			c.pubCalled = true
			return nil
		},
		CapUSD:     func(context.Context, string) (float64, error) { return 1.0, nil },
		Now:        func() time.Time { return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC) },
		NewDraftID: func() string { return "draft-1" },
	}
}

func makeEnv() pkgevents.Envelope[WebsiteApprovedDetail] {
	return pkgevents.New("website.approved", "api-website", WebsiteApprovedDetail{
		BusinessID: "biz-1", WebsiteID: "web-1",
	})
}

func validWebsite() *WebsiteRow {
	return &WebsiteRow{ID: "web-1", Status: "approved", PreviewURL: "https://p.example.com/sites/web-1", PasscodeCipher: "cipher-blob"}
}

func TestRunOne_SubstitutesCleartextAndDoesNotLeakIt(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	if err := runOne(context.Background(), newDeps(t, c, validWebsite()), makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if !c.putCalled {
		t.Fatal("EmailDraft not persisted")
	}
	if !strings.Contains(c.putRow.Body, testCleartext) {
		t.Errorf("cleartext not substituted into persisted body: %q", c.putRow.Body)
	}
	if strings.Contains(c.putRow.Body, schemas.PasscodePlaceholder) {
		t.Errorf("placeholder still present after substitution: %q", c.putRow.Body)
	}
	if c.putRow.Status != "draft" || c.putRow.PromptID != "email.v1" || c.putRow.ContactID != "con-1" {
		t.Errorf("draft row drift: %+v", c.putRow)
	}
	// outreach.email_ready must NOT carry the body or the cleartext.
	evJSON, _ := json.Marshal(c.published)
	if strings.Contains(string(evJSON), testCleartext) {
		t.Fatalf("cleartext leaked into outreach.email_ready: %s", evJSON)
	}
	if c.published.Detail.DraftID != "draft-1" || c.published.Detail.WordCount != 65 {
		t.Errorf("event detail drift: %+v", c.published.Detail)
	}
}

func TestRunOne_CipherWiped_NoDraft(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	web := validWebsite()
	web.PasscodeCipher = "" // wiped
	if err := runOne(context.Background(), newDeps(t, c, web), makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne should not error on wiped cipher (terminal skip): %v", err)
	}
	if c.putCalled || c.pubCalled {
		t.Error("no draft/event must be produced when passcodeCipher is wiped")
	}
}

func TestRunOne_MissingWebsiteOrBusinessSkips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, nil) // GetWebsite returns nil
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if c.putCalled {
		t.Error("missing website must be a no-op")
	}
}

func TestHandle_KillSwitchOff_NoWork(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	body, _ := json.Marshal(makeEnv())
	eb := lambdaevents.EventBridgeEvent{Source: pkgevents.Source, DetailType: "website.approved", Detail: body}
	raw, _ := json.Marshal(eb)
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{{MessageId: "m1", Body: string(raw)}}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill-switch-off must succeed silently, got %v", resp.BatchItemFailures)
	}
}
