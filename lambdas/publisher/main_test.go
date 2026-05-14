package main

import (
	"context"
	"encoding/json"
	"errors"
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

// --- DDB fake ----------------------------------------------------------

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

func setupItemsTable(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func setupKillSwitch(t *testing.T, on bool) {
	t.Helper()
	s := killswitch.Defaults()
	s.Stages.PreviewEnabled = on
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

// --- fixtures + captured -----------------------------------------------

type captured struct {
	r2Key       string
	r2Body      []byte
	kvKey       string
	kvValue     string
	kvMeta      map[string]string
	rowUpdated  WebsiteRow
	publishedOK bool
	published   pkgevents.Envelope[WebsitePublishedDetail]
	encryptArg  string
	hashArg     string
	r2Err       error
	kvErr       error
	encryptErr  error
	publishErr  error
}

func validBiz() *BusinessRow {
	return &BusinessRow{ID: "biz-1", Name: "Acme", Domain: "acme.co.uk", Vertical: "trades"}
}

func validWebsite() *WebsiteRow {
	return &WebsiteRow{
		PK: "BUSINESS#biz-1", SK: "WEBSITE#website-1", Type: "Website",
		ID: "website-1", SpecID: "spec-1", R2Prefix: "sites/website-1/",
		Status: "generated", CreatedAt: "2026-05-14T11:00:00Z",
		UpdatedAt: "2026-05-14T11:00:00Z", Etag: "seed",
	}
}

func newDeps(t *testing.T, c *captured, biz *BusinessRow, web *WebsiteRow) runDeps {
	t.Helper()
	return runDeps{
		GetBusiness: func(context.Context, string) (*BusinessRow, error) { return biz, nil },
		GetWebsite:  func(context.Context, string, string) (*WebsiteRow, error) { return web, nil },
		GetHTML:     func(context.Context, string) ([]byte, error) { return []byte("<!doctype html><html></html>"), nil },
		PutR2: func(_ context.Context, key string, body []byte) error {
			c.r2Key = key
			c.r2Body = body
			return c.r2Err
		},
		GeneratePass: func() (string, error) { return "ABCD2345", nil },
		HashPass: func(p, _ string) string {
			c.hashArg = p
			return "hash-of-" + p
		},
		EncryptPass: func(_ context.Context, cleartext string) (string, error) {
			c.encryptArg = cleartext
			if c.encryptErr != nil {
				return "", c.encryptErr
			}
			return "cipher-of-" + cleartext, nil
		},
		PutKV: func(_ context.Context, key, value string, metadata map[string]string) error {
			c.kvKey = key
			c.kvValue = value
			c.kvMeta = metadata
			return c.kvErr
		},
		UpdateRow: func(_ context.Context, row WebsiteRow) error {
			c.rowUpdated = row
			return nil
		},
		Publish: func(_ context.Context, env pkgevents.Envelope[WebsitePublishedDetail]) error {
			c.publishedOK = true
			c.published = env
			return c.publishErr
		},
		Now:            func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
		PasscodeSalt:   "test-salt",
		PreviewURLBase: "https://previews.example.com",
	}
}

func makeEnv() pkgevents.Envelope[WebsiteGeneratedDetail] {
	return pkgevents.New("website.generated", "generator", WebsiteGeneratedDetail{
		BusinessID: "biz-1", SpecID: "spec-1", WebsiteID: "website-1",
		S3Key: "generated/website-1/index.html",
	})
}

// --- happy path --------------------------------------------------------

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validWebsite())
	env := makeEnv()
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.r2Key != "sites/website-1/index.html" {
		t.Errorf("R2 key drift: %q", c.r2Key)
	}
	if !strings.Contains(string(c.r2Body), "<!doctype html>") {
		t.Errorf("R2 body drift")
	}
	if c.hashArg != "ABCD2345" || c.encryptArg != "ABCD2345" {
		t.Errorf("passcode wasn't routed cleartext-into-hash/encrypt: hash=%q encrypt=%q", c.hashArg, c.encryptArg)
	}
	if c.kvKey != "passcode:website-1" || c.kvValue != "hash-of-ABCD2345" {
		t.Errorf("KV write drift: key=%q value=%q", c.kvKey, c.kvValue)
	}
	if c.kvMeta["businessId"] != "biz-1" || c.kvMeta["websiteId"] != "website-1" {
		t.Errorf("KV metadata drift: %+v", c.kvMeta)
	}
	if c.rowUpdated.Status != "published" {
		t.Errorf("status not flipped: %q", c.rowUpdated.Status)
	}
	if c.rowUpdated.PasscodeHash != "hash-of-ABCD2345" {
		t.Errorf("passcodeHash drift: %q", c.rowUpdated.PasscodeHash)
	}
	if c.rowUpdated.PasscodeCipher != "cipher-of-ABCD2345" {
		t.Errorf("passcodeCipher drift: %q", c.rowUpdated.PasscodeCipher)
	}
	wantRevealableUntil := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC).Add(7 * 24 * time.Hour).Unix()
	if c.rowUpdated.PasscodeRevealableUntil != wantRevealableUntil {
		t.Errorf("passcodeRevealableUntil drift: got %d want %d", c.rowUpdated.PasscodeRevealableUntil, wantRevealableUntil)
	}
	if c.rowUpdated.PreviewURL != "https://previews.example.com/sites/website-1" {
		t.Errorf("previewURL drift: %q", c.rowUpdated.PreviewURL)
	}
	if !c.publishedOK || c.published.EventName != "website.published" {
		t.Errorf("publish drift")
	}
	if c.published.Detail.PreviewURL == "" || !c.published.Detail.PasscodeIssued {
		t.Errorf("published detail drift: %+v", c.published.Detail)
	}
}

// --- cleartext does not appear in the event payload -------------------

func TestRunOne_PublishedEventDoesNotCarryCleartext(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validWebsite())
	if err := processRecord(context.Background(), d, makeEnv()); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	body, _ := json.Marshal(c.published)
	if strings.Contains(string(body), "ABCD2345") {
		t.Fatalf("cleartext passcode leaked in event payload: %s", string(body))
	}
}

// --- defensive guard: not-generated status skips ---------------------

func TestRunOne_NonGeneratedStatus_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	web := validWebsite()
	web.Status = "published" // already done
	d := newDeps(t, c, validBiz(), web)
	if err := processRecord(context.Background(), d, makeEnv()); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.r2Key != "" || c.kvKey != "" || c.publishedOK {
		t.Errorf("expected no-op on non-generated website; r2=%q kv=%q pub=%v", c.r2Key, c.kvKey, c.publishedOK)
	}
}

// --- missing rows ------------------------------------------------------

func TestRunOne_BusinessMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, nil, validWebsite())
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv()); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedOK {
		t.Error("expected no publish")
	}
}

func TestRunOne_WebsiteMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), nil)
	d.GetWebsite = func(context.Context, string, string) (*WebsiteRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv()); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedOK {
		t.Error("expected no publish")
	}
}

// --- error surfaces (→ SQS retry → DLQ) ------------------------------

func TestRunOne_R2Error_NoPublishNoStatusFlip(t *testing.T) {
	setupItemsTable(t)
	c := &captured{r2Err: errors.New("r2 down")}
	d := newDeps(t, c, validBiz(), validWebsite())
	if err := processRecord(context.Background(), d, makeEnv()); err == nil {
		t.Fatal("expected R2 error to surface")
	}
	if c.publishedOK {
		t.Error("publish should not fire on R2 error")
	}
	if c.rowUpdated.Status == "published" {
		t.Error("status should not flip on R2 error")
	}
}

func TestRunOne_EncryptError_NoKVWrite(t *testing.T) {
	setupItemsTable(t)
	c := &captured{encryptErr: errors.New("kms down")}
	d := newDeps(t, c, validBiz(), validWebsite())
	if err := processRecord(context.Background(), d, makeEnv()); err == nil {
		t.Fatal("expected encrypt error to surface")
	}
	if c.kvKey != "" {
		t.Error("KV write should not fire when KMS encrypt fails — we'd otherwise leak a hash for an unrecoverable passcode")
	}
}

func TestRunOne_KVError_NoStatusFlip(t *testing.T) {
	setupItemsTable(t)
	c := &captured{kvErr: errors.New("cloudflare down")}
	d := newDeps(t, c, validBiz(), validWebsite())
	if err := processRecord(context.Background(), d, makeEnv()); err == nil {
		t.Fatal("expected KV error to surface")
	}
	if c.rowUpdated.Status == "published" {
		t.Error("status should not flip on KV failure")
	}
}

func TestRunOne_PublishError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{publishErr: errors.New("eventbridge down")}
	d := newDeps(t, c, validBiz(), validWebsite())
	if err := processRecord(context.Background(), d, makeEnv()); err == nil {
		t.Fatal("expected publish error to surface")
	}
	// Row IS updated before publish — the artifact is live + the
	// passcode is in KV. SQS retry redoes the publish.
	if c.rowUpdated.Status != "published" {
		t.Error("row should be updated before publish attempt")
	}
}

// --- idempotent replay -----------------------------------------------

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validWebsite())
	env := makeEnv()
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	c.r2Key = "" // clear to detect re-do
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.r2Key != "" {
		t.Errorf("replay should be no-op; r2Key re-set to %q", c.r2Key)
	}
}

// --- killswitch off + bad body --------------------------------------

func TestHandle_KillSwitchOff_NoWork(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	rec := makeSQSRecord(t, makeEnv())
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill switch off should yield no failures, got %v", resp.BatchItemFailures)
	}
}

func TestHandle_BadBody_BatchFailure(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, true)
	t.Setenv("EVENT_BUS_NAME", "test-bus")
	t.Setenv("BLOBS_BUCKET", "test")
	t.Setenv("R2_ACCOUNT_ID", "x")
	t.Setenv("R2_BUCKET", "x")
	t.Setenv("R2_ACCESS_KEY_ID", "x")
	t.Setenv("R2_SECRET_ACCESS_KEY", "x")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "x")
	t.Setenv("CLOUDFLARE_KV_NAMESPACE_ID", "x")
	t.Setenv("CLOUDFLARE_API_TOKEN", "x")
	t.Setenv("PASSCODE_SALT", "x")
	t.Setenv("PASSCODE_KMS_KEY_ID", "x")
	t.Setenv("PREVIEW_URL_BASE", "https://x")
	rec := lambdaevents.SQSMessage{MessageId: "msg-bad", Body: "not json"}
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		return
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("expected one BatchItemFailure, got %+v", resp.BatchItemFailures)
	}
}

// --- helpers --------------------------------------------------------

func makeSQSRecord(t *testing.T, env pkgevents.Envelope[WebsiteGeneratedDetail]) lambdaevents.SQSMessage {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	eb := lambdaevents.EventBridgeEvent{
		Source:     pkgevents.Source,
		DetailType: env.EventName,
		Detail:     body,
	}
	raw, err := json.Marshal(eb)
	if err != nil {
		t.Fatalf("marshal eb: %v", err)
	}
	return lambdaevents.SQSMessage{MessageId: env.EventID, Body: string(raw)}
}
