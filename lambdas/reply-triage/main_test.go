package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

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

const passcodeLeak = "SECRET-PASSCODE-9Z7"

func mimeReply(from, to, body string) string {
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: Re: preview\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n"
}

type capture struct {
	suppressed []string
	suppRows   int
	statusBiz  string
	statusVal  string
	triage     TriageRow
	published  TriagedDetail
	pubCalled  bool
}

func deps(c *capture, ref *refIndex, cls schemas.ReplyTriageV1, clsErr error) runDeps {
	return runDeps{
		GetMail:   func(context.Context, string, string) ([]byte, error) { return nil, nil },
		LookupRef: func(context.Context, string) (*refIndex, error) { return ref, nil },
		Classify:  func(context.Context, string) (schemas.ReplyTriageV1, error) { return cls, clsErr },
		Suppress: func(_ context.Context, email, _ string) error {
			c.suppressed = append(c.suppressed, email)
			return nil
		},
		PutSuppression: func(context.Context, string, string, string) error { c.suppRows++; return nil },
		SetStatus: func(_ context.Context, biz, status, _ string) error {
			c.statusBiz, c.statusVal = biz, status
			return nil
		},
		PutTriage: func(_ context.Context, r TriageRow) error { c.triage = r; return nil },
		Publish: func(_ context.Context, d TriagedDetail) error {
			c.published, c.pubCalled = d, true
			return nil
		},
		Now:         func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) },
		ReplyDomain: "outreach.example.com",
	}
}

// runOne reads via d.GetMail; override per test.
func withMail(d runDeps, raw string) runDeps {
	d.GetMail = func(context.Context, string, string) ([]byte, error) { return []byte(raw), nil }
	return d
}

func ref1() *refIndex {
	return &refIndex{BusinessID: "biz-1", DraftID: "draft-1", WebsiteID: "web-1", ContactID: "con-1"}
}

func TestRunOne_UnsubscribeHighConf_SuppressesAndRejects(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "unsubscribe", Confidence: 0.95, Rationale: "said remove me"}, nil),
		mimeReply("Jane <jane@acme.co.uk>", "outreach+draft-1@outreach.example.com", "please remove me, not interested"))
	if err := processRecord(context.Background(), d, "bkt", "inbound/k1"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 1 || c.suppressed[0] != "jane@acme.co.uk" || c.suppRows != 1 {
		t.Errorf("suppression drift: %v rows=%d", c.suppressed, c.suppRows)
	}
	if c.statusBiz != "biz-1" || c.statusVal != statusRejected {
		t.Errorf("status drift: %q %q", c.statusBiz, c.statusVal)
	}
	if c.triage.TriageState != statusActioned || c.triage.PK != "BUSINESS#biz-1" || c.triage.GSI1PK != "REPLYTRIAGE#STATUS#auto_actioned" {
		t.Errorf("triage row drift: %+v", c.triage)
	}
}

func TestRunOne_UnsubscribeLowConf_ToOperator(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "unsubscribe", Confidence: 0.6}, nil),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", "hmm maybe not"))
	if err := processRecord(context.Background(), d, "b", "inbound/k2"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if len(c.suppressed) != 0 || c.statusVal != "" {
		t.Error("low-confidence unsubscribe must not auto-suppress or change status")
	}
	if c.triage.TriageState != statusInbox || c.triage.GSI1PK != "REPLYTRIAGE#STATUS#operator_inbox" {
		t.Errorf("must go to operator inbox: %+v", c.triage)
	}
}

func TestRunOne_PositiveInterest_MarksResponded(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "positive_interest", Confidence: 0.8}, nil),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", "looks great, how much?"))
	if err := processRecord(context.Background(), d, "b", "inbound/k3"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.statusVal != statusResponded || c.statusBiz != "biz-1" {
		t.Errorf("expected responded: %q %q", c.statusBiz, c.statusVal)
	}
	if len(c.suppressed) != 0 || c.triage.TriageState != statusActioned {
		t.Errorf("interest must not suppress; state drift: %+v", c.triage)
	}
}

func TestRunOne_Unknown_ToOperatorInbox(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "unknown", Confidence: 0.4}, nil),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", "who is this?"))
	if err := processRecord(context.Background(), d, "b", "inbound/k4"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.triage.TriageState != statusInbox || c.statusVal != "" || len(c.suppressed) != 0 {
		t.Errorf("unknown must park for operator only: %+v", c.triage)
	}
}

func TestRunOne_BudgetCap_DegradesToInbox(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{}, cost.ErrBudgetCapExceeded),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", "interested!"))
	if err := processRecord(context.Background(), d, "b", "inbound/k5"); err != nil {
		t.Fatalf("budget cap must not error: %v", err)
	}
	if c.triage.TriageState != statusInbox || c.triage.Category != schemas.ReplyCategoryUnknown {
		t.Errorf("cap should park as unknown/operator: %+v", c.triage)
	}
}

func TestRunOne_Unattributed_GlobalInbox(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, nil, schemas.ReplyTriageV1{Category: "unknown", Confidence: 0.3}, nil),
		mimeReply("j@acme.co.uk", "outreach@outreach.example.com", "??"))
	if err := processRecord(context.Background(), d, "b", "inbound/k6"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.triage.PK != "REPLYTRIAGE#INBOX" || c.triage.BusinessID != "" || c.statusVal != "" {
		t.Errorf("unattributed must land in global inbox with no business action: %+v", c.triage)
	}
}

func TestRunOne_UnparseableMIME_Dropped(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, nil, schemas.ReplyTriageV1{}, nil), "\x00 not mime")
	if err := processRecord(context.Background(), d, "b", "inbound/k7"); err != nil {
		t.Errorf("unparseable must drop (nil): %v", err)
	}
	if c.pubCalled {
		t.Error("nothing should be published for poison input")
	}
}

func TestRunOne_PrivacyAndQuotedStrip(t *testing.T) {
	setup(t)
	c := &capture{}
	body := "Yes please, very interested.\n\nOn Tue, Acme wrote:\n> use access code " + passcodeLeak + "\n> https://x"
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "positive_interest", Confidence: 0.9}, nil),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", body))
	if err := processRecord(context.Background(), d, "b", "inbound/k8"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	// Quoted original (which echoes the cleartext) must be stripped from the excerpt.
	if strings.Contains(c.triage.BodyExcerpt, passcodeLeak) {
		t.Fatalf("excerpt leaked quoted passcode: %q", c.triage.BodyExcerpt)
	}
	if !strings.Contains(c.triage.BodyExcerpt, "very interested") {
		t.Errorf("sender's own text should survive: %q", c.triage.BodyExcerpt)
	}
	// The published event must carry ids/label only — never the reply text.
	b, _ := json.Marshal(c.published)
	if strings.Contains(string(b), "interested") || strings.Contains(string(b), passcodeLeak) {
		t.Fatalf("reply.triaged leaked body: %s", b)
	}
}

func TestRunOne_IdempotentReplay(t *testing.T) {
	setup(t)
	c := &capture{}
	d := withMail(deps(c, ref1(), schemas.ReplyTriageV1{Category: "unknown", Confidence: 0.2}, nil),
		mimeReply("j@acme.co.uk", "outreach+draft-1@outreach.example.com", "?"))
	if err := processRecord(context.Background(), d, "b", "dupkey"); err != nil {
		t.Fatalf("first: %v", err)
	}
	c.pubCalled = false
	if err := processRecord(context.Background(), d, "b", "dupkey"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.pubCalled {
		t.Error("replay must be a no-op")
	}
}

func TestPlusTokenAndRedact(t *testing.T) {
	if plusToken("outreach+d1@outreach.example.com", "outreach.example.com") != "d1" {
		t.Error("plusToken")
	}
	if plusToken("outreach+d1@evil.com", "outreach.example.com") != "" {
		t.Error("wrong domain must yield no token")
	}
	if got := redact("jane@acme.co.uk"); got != "***@acme.co.uk" || strings.Contains(got, "jane") {
		t.Errorf("redact leaked local part: %q", got)
	}
}
