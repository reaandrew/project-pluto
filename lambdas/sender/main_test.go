package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
)

func newTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeDDB struct {
	idempSeen map[string]bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if in.ConditionExpression != nil && strings.Contains(*in.ConditionExpression, "attribute_not_exists(pk)") {
		if pk, ok := in.Item["pk"].(*dtypes.AttributeValueMemberS); ok {
			sk := ""
			if s, ok := in.Item["sk"].(*dtypes.AttributeValueMemberS); ok {
				sk = s.Value
			}
			key := pk.Value + "|" + sk
			if f.idempSeen == nil {
				f.idempSeen = map[string]bool{}
			}
			if f.idempSeen[key] {
				return nil, &dtypes.ConditionalCheckFailedException{}
			}
			f.idempSeen[key] = true
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
	sentRaw    []byte
	sentCalled bool
	markerKey  string
	eventRow   EmailEventRow
	eventPut   bool
	msgIndexes []SESMsgIndexRow
	draftSet   bool
	published  pkgevents.Envelope[EmailSentDetail]
	pubCalled  bool
}

func newDeps(t *testing.T, c *captured, draftStatus, contactEmail string, suppressed bool, supErr error, firstSend bool) runDeps {
	t.Helper()
	return runDeps{
		GetDraft: func(context.Context, string, string) (*EmailDraftRow, error) {
			return &EmailDraftRow{ID: "draft-1", Subject: "Quick preview for Acme", Body: "Hi Jane,\nUse access code H7Q32KX9.\nReply 'no thanks'.", Status: draftStatus}, nil
		},
		GetContact: func(context.Context, string, string) (*ContactRow, error) {
			return &ContactRow{ID: "con-1", Email: contactEmail}, nil
		},
		MarkSentOnce: func(_ context.Context, _, key string) (bool, error) {
			c.markerKey = key
			return firstSend, nil
		},
		IsSuppressed: func(context.Context, string) (bool, error) { return suppressed, supErr },
		Send: func(_ context.Context, raw []byte) (string, error) {
			c.sentCalled = true
			c.sentRaw = raw
			return "ses-msg-1", nil
		},
		PutEvent: func(_ context.Context, row EmailEventRow) error { c.eventRow = row; c.eventPut = true; return nil },
		PutMsgIndex: func(_ context.Context, idx SESMsgIndexRow) error {
			c.msgIndexes = append(c.msgIndexes, idx)
			return nil
		},
		SetDraftSent: func(context.Context, string, string, string) error { c.draftSet = true; return nil },
		Publish: func(_ context.Context, env pkgevents.Envelope[EmailSentDetail]) error {
			c.published = env
			c.pubCalled = true
			return nil
		},
		CapUSD:      func(context.Context) (float64, error) { return 1.0, nil },
		Now:         func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) },
		FromAddress: "outreach@outreach.example.com",
		UnsubBase:   "https://api.example.com",
	}
}

func makeEnv() pkgevents.Envelope[EmailApprovedDetail] {
	return pkgevents.New("email.approved", "api-email", EmailApprovedDetail{
		BusinessID: "biz-1", DraftID: "draft-1", WebsiteID: "web-1", ContactID: "con-1",
	})
}

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "approved", "jane@acme.co.uk", false, nil, true)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if !c.sentCalled {
		t.Fatal("SES send not called")
	}
	raw := string(c.sentRaw)
	for _, want := range []string{
		"From: outreach@outreach.example.com",
		"To: jane@acme.co.uk",
		"Reply-To: outreach+draft-1@outreach.example.com",
		"List-Unsubscribe: <https://api.example.com/unsubscribe?d=draft-1>, <mailto:outreach@outreach.example.com?subject=unsubscribe>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"Use access code H7Q32KX9.",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw MIME missing %q\n---\n%s", want, raw)
		}
	}
	if !c.eventPut || c.eventRow.Event != "sent" || c.eventRow.SESMessageID != "ses-msg-1" {
		t.Errorf("EmailEvent drift: %+v", c.eventRow)
	}
	if !c.draftSet {
		t.Error("draft status not flipped to sent")
	}
	if len(c.msgIndexes) != 2 {
		t.Fatalf("want 2 reverse-index writes (SESMSG + REPLYREF), got %d: %+v", len(c.msgIndexes), c.msgIndexes)
	}
	if c.msgIndexes[0].PK != "SESMSG#ses-msg-1" || c.msgIndexes[0].BusinessID != "biz-1" || c.msgIndexes[0].DraftID != "draft-1" {
		t.Errorf("SES msg reverse-index drift: %+v", c.msgIndexes[0])
	}
	if c.msgIndexes[1].PK != "REPLYREF#draft-1" || c.msgIndexes[1].Type != "ReplyRefIndex" || c.msgIndexes[1].BusinessID != "biz-1" || c.msgIndexes[1].ContactID != "con-1" {
		t.Errorf("reply-ref reverse-index drift: %+v", c.msgIndexes[1])
	}
	if !c.pubCalled || c.published.Detail.SESMessageID != "ses-msg-1" {
		t.Errorf("email.sent not published correctly: %+v", c.published.Detail)
	}
	// email.sent must carry no body / no address.
	evJSON, _ := json.Marshal(c.published)
	if strings.Contains(string(evJSON), "jane@acme.co.uk") || strings.Contains(string(evJSON), "access code") {
		t.Fatalf("email.sent leaked address/body: %s", evJSON)
	}
}

func TestRunOne_SuppressedNeverSends(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "approved", "blocked@acme.co.uk", true, nil, true)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if c.sentCalled || c.markerKey != "" || c.eventPut || c.pubCalled {
		t.Error("suppressed recipient must not be sent, marked, or evented")
	}
}

func TestRunOne_SuppressionErrorFailsClosed(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "approved", "j@acme.co.uk", false, errors.New("ses throttled"), true)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err == nil {
		t.Fatal("an unknown suppression-check error must abort (fail closed), not send")
	}
	if c.sentCalled {
		t.Error("must not send when suppression status is unknown")
	}
}

func TestRunOne_AlreadySent_NoSecondSend(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "approved", "j@acme.co.uk", false, nil, false) // marker already present
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if c.sentCalled {
		t.Error("same (contactId,websiteId) must not send twice")
	}
}

func TestRunOne_NotApprovedSkips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "rejected", "j@acme.co.uk", false, nil, true)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if c.sentCalled {
		t.Error("non-approved draft must not be sent")
	}
}

func TestRunOne_NoContactEmailSkips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, "approved", "", false, nil, true)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if c.sentCalled {
		t.Error("must not send with no contact email")
	}
}

func TestSendDedupKey_StableAndDistinct(t *testing.T) {
	a := sendDedupKey("con-1", "web-1")
	if a != sendDedupKey("con-1", "web-1") {
		t.Error("dedup key must be stable")
	}
	if a == sendDedupKey("con-1", "web-2") || a == sendDedupKey("con-2", "web-1") {
		t.Error("dedup key must vary by contact and website")
	}
}

func TestPlusAddress(t *testing.T) {
	if got := plusAddress("outreach@outreach.example.com", "d-123"); got != "outreach+d-123@outreach.example.com" {
		t.Errorf("plusAddress: %q", got)
	}
	// Token must not be able to break the address or inject a header.
	if got := plusAddress("o@x.com", "a@b\r\nBcc: e+f"); strings.ContainsAny(got, "\r\n") || strings.Count(got, "@") != 1 {
		t.Errorf("plusAddress not sanitised: %q", got)
	}
	if got := plusAddress("noatsign", "x"); got != "noatsign" {
		t.Errorf("plusAddress should no-op on a malformed address: %q", got)
	}
}

func TestSanitiseHeader_StripsCRLF(t *testing.T) {
	if got := sanitiseHeader("evil\r\nBcc: attacker@x.com"); strings.ContainsAny(got, "\r\n") {
		t.Errorf("header injection not stripped: %q", got)
	}
}

func TestHandle_KillSwitchOff_NoWork(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	body, _ := json.Marshal(makeEnv())
	eb := lambdaevents.EventBridgeEvent{Source: pkgevents.Source, DetailType: "email.approved", Detail: body}
	raw, _ := json.Marshal(eb)
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{{MessageId: "m1", Body: string(raw)}}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill-switch-off must succeed silently, got %v", resp.BatchItemFailures)
	}
}
