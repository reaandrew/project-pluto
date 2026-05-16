package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
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

type captured struct {
	respondedBiz string
	respondedN   int
	event        EmailEventRow
	eventPut     bool
	published    ReplyDetail
	pubCalled    bool
}

func newDeps(c *captured, raw string, ref *refIndex) runDeps {
	return runDeps{
		GetMail:   func(context.Context, string, string) ([]byte, error) { return []byte(raw), nil },
		LookupRef: func(context.Context, string) (*refIndex, error) { return ref, nil },
		MarkResponded: func(_ context.Context, biz, _ string) error {
			c.respondedBiz = biz
			c.respondedN++
			return nil
		},
		PutEvent:    func(_ context.Context, row EmailEventRow) error { c.event = row; c.eventPut = true; return nil },
		Publish:     func(_ context.Context, d ReplyDetail) error { c.published = d; c.pubCalled = true; return nil },
		Now:         func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) },
		ReplyDomain: "outreach.example.com",
	}
}

const secret = "please call me about the redesign, my number is 0700900900"

func mimeTo(to string) string {
	return "From: jane@acme.co.uk\r\nTo: " + to + "\r\nSubject: Re: your message\r\n\r\n" + secret + "\r\n"
}

func ref1() *refIndex {
	return &refIndex{BusinessID: "biz-1", DraftID: "draft-1", WebsiteID: "web-1", ContactID: "con-1"}
}

func TestRunOne_HappyPath(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, mimeTo("outreach+draft-1@outreach.example.com"), ref1())
	if err := processRecord(context.Background(), d, "inbound-bkt", "inbound/abc"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.respondedBiz != "biz-1" || c.respondedN != 1 {
		t.Errorf("MarkResponded drift: %q n=%d", c.respondedBiz, c.respondedN)
	}
	if !c.eventPut || c.event.Event != "replied" || c.event.PK != "BUSINESS#biz-1" || c.event.DraftID != "draft-1" {
		t.Errorf("EmailEvent drift: %+v", c.event)
	}
	if !c.pubCalled || c.published.BusinessID != "biz-1" || c.published.ContactID != "con-1" {
		t.Errorf("publish drift: %+v", c.published)
	}
	// The reply body must never travel in the event row or the payload.
	evJSON, _ := json.Marshal(struct {
		E EmailEventRow
		P ReplyDetail
	}{c.event, c.published})
	if strings.Contains(string(evJSON), "0700900900") || strings.Contains(string(evJSON), "call me") {
		t.Fatalf("reply body leaked into event/payload: %s", evJSON)
	}
}

func TestRunOne_AttributionViaDeliveredTo(t *testing.T) {
	setup(t)
	c := &captured{}
	raw := "From: jane@acme.co.uk\r\nTo: jane@acme.co.uk\r\n" +
		"Delivered-To: outreach+draft-9@outreach.example.com\r\nSubject: hi\r\n\r\nhello\r\n"
	d := newDeps(c, raw, &refIndex{BusinessID: "biz-9", DraftID: "draft-9"})
	if err := processRecord(context.Background(), d, "b", "k"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.respondedBiz != "biz-9" {
		t.Errorf("want biz-9 via Delivered-To, got %q", c.respondedBiz)
	}
}

func TestRunOne_NoPlusToken_Unattributed(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, mimeTo("outreach@outreach.example.com"), ref1())
	if err := processRecord(context.Background(), d, "b", "k"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.respondedN != 0 || c.pubCalled {
		t.Error("no +token ⇒ must not mark responded or publish")
	}
}

func TestRunOne_WrongDomain_Ignored(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, mimeTo("outreach+draft-1@evil.example.net"), ref1())
	if err := processRecord(context.Background(), d, "b", "k"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.respondedN != 0 {
		t.Error("plus-addressed but wrong domain must be ignored")
	}
}

func TestRunOne_RefMissing_Unattributed(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, mimeTo("outreach+draft-x@outreach.example.com"), nil)
	if err := processRecord(context.Background(), d, "b", "k"); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.respondedN != 0 || c.pubCalled {
		t.Error("missing reply-ref ⇒ drop, no status change")
	}
}

func TestRunOne_UnparseableMIME_Dropped(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, "\x00 not a message", ref1())
	if err := processRecord(context.Background(), d, "b", "k"); err != nil {
		t.Errorf("unparseable MIME must be dropped (nil), got %v", err)
	}
	if c.respondedN != 0 {
		t.Error("unparseable ⇒ no work")
	}
}

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setup(t)
	c := &captured{}
	d := newDeps(c, mimeTo("outreach+draft-1@outreach.example.com"), ref1())
	if err := processRecord(context.Background(), d, "b", "dup-key"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := processRecord(context.Background(), d, "b", "dup-key"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.respondedN != 1 {
		t.Errorf("replay must be a no-op, respondedN=%d", c.respondedN)
	}
}

func TestPlusToken(t *testing.T) {
	if got := plusToken("outreach+d1@outreach.example.com", "outreach.example.com"); got != "d1" {
		t.Errorf("plusToken: %q", got)
	}
	if got := plusToken("outreach+d1@OUTREACH.EXAMPLE.COM", "outreach.example.com"); got != "d1" {
		t.Errorf("plusToken should be domain case-insensitive: %q", got)
	}
	if got := plusToken("plain@outreach.example.com", "outreach.example.com"); got != "" {
		t.Errorf("no + ⇒ empty, got %q", got)
	}
	if got := plusToken("outreach+@outreach.example.com", "outreach.example.com"); got != "" {
		t.Errorf("empty token ⇒ empty, got %q", got)
	}
}
