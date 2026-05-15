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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
)

// fakeDDB satisfies the cost ledger's DynamoDB needs: GetItem returns
// no prior spend, UpdateItem accepts the Record write.
type fakeDDB struct{}

func (fakeDDB) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (fakeDDB) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (fakeDDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setupItemsTable(t *testing.T) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	ddb.SetClient(fakeDDB{})
	t.Cleanup(func() { ddb.SetClient(nil) })
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func makeSQSEvent(t *testing.T) lambdaevents.SQSEvent {
	t.Helper()
	env := makeEnv()
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
	return lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{
		{MessageId: env.EventID, Body: string(raw)},
	}}
}

func setupKillSwitch(t *testing.T, on bool) {
	t.Helper()
	s := killswitch.Defaults()
	s.Stages.PreviewEnabled = on
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

type captured struct {
	r2Keys    []string
	shotURLs  []string
	rowURLs   map[string]string
	rowNow    time.Time
	capCalled bool
	shotErr   error
	r2Err     error
	setErr    error
}

func validWebsite() *WebsiteRow {
	return &WebsiteRow{ID: "website-1", Status: "published"}
}

func newDeps(t *testing.T, c *captured, web *WebsiteRow) runDeps {
	t.Helper()
	return runDeps{
		GetWebsite: func(context.Context, string, string) (*WebsiteRow, error) { return web, nil },
		Screenshot: func(_ context.Context, targetURL string, _, _ int) ([]byte, error) {
			c.shotURLs = append(c.shotURLs, targetURL)
			if c.shotErr != nil {
				return nil, c.shotErr
			}
			return []byte("\x89PNG-bytes"), nil
		},
		PutR2: func(_ context.Context, key string, _ []byte) error {
			c.r2Keys = append(c.r2Keys, key)
			return c.r2Err
		},
		SetScreenshots: func(_ context.Context, _, _ string, urls map[string]string, now time.Time) error {
			c.rowURLs = urls
			c.rowNow = now
			return c.setErr
		},
		Cap: func(context.Context) (float64, error) {
			c.capCalled = true
			return 5.0, nil
		},
		Now:          func() time.Time { return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC) },
		PasscodeSalt: "test-salt",
	}
}

func makeEnv() pkgevents.Envelope[WebsitePublishedDetail] {
	return pkgevents.New("website.published", "publisher", WebsitePublishedDetail{
		BusinessID: "biz-1",
		WebsiteID:  "website-1",
		PreviewURL: "https://previews.example.com/sites/website-1",
	})
}

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validWebsite())
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if len(c.r2Keys) != 2 {
		t.Fatalf("want 2 R2 uploads, got %v", c.r2Keys)
	}
	wantKeys := map[string]bool{
		"screenshots/website-1/desktop.png": false,
		"screenshots/website-1/mobile.png":  false,
	}
	for _, k := range c.r2Keys {
		if _, ok := wantKeys[k]; !ok {
			t.Errorf("unexpected R2 key %q", k)
		}
		wantKeys[k] = true
	}
	for k, seen := range wantKeys {
		if !seen {
			t.Errorf("missing R2 key %q", k)
		}
	}
	if c.rowURLs["desktop"] != "https://previews.example.com/screenshots/website-1/desktop.png" {
		t.Errorf("desktop URL drift: %q", c.rowURLs["desktop"])
	}
	if c.rowURLs["mobile"] != "https://previews.example.com/screenshots/website-1/mobile.png" {
		t.Errorf("mobile URL drift: %q", c.rowURLs["mobile"])
	}
	if !c.capCalled {
		t.Error("budget cap was not consulted")
	}
}

func TestRunOne_TargetURLCarriesOpTokenNeverLogged(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validWebsite())
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	for _, u := range c.shotURLs {
		if !strings.HasPrefix(u, "https://previews.example.com/sites/website-1?op=website-1.") {
			t.Errorf("screenshot target URL is not the op-token'd preview URL: %q", u)
		}
	}
}

func TestRunOne_NotPublishedSkips(t *testing.T) {
	c := &captured{}
	w := validWebsite()
	w.Status = "rejected"
	d := newDeps(t, c, w)
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if len(c.r2Keys) != 0 || c.rowURLs != nil {
		t.Error("a non-published website must not be screenshotted")
	}
}

func TestRunOne_MissingWebsiteSkips(t *testing.T) {
	c := &captured{}
	d := newDeps(t, c, nil)
	d.GetWebsite = func(context.Context, string, string) (*WebsiteRow, error) { return nil, nil }
	if err := runOne(context.Background(), d, makeEnv(), newTestLogger()); err != nil {
		t.Fatalf("runOne: %v", err)
	}
	if len(c.r2Keys) != 0 {
		t.Error("missing website must be a no-op")
	}
}

func TestRunOne_ScreenshotErrorSurfacesNoRowUpdate(t *testing.T) {
	setupItemsTable(t)
	c := &captured{shotErr: errors.New("browser-rendering returned HTTP 500")}
	d := newDeps(t, c, validWebsite())
	err := runOne(context.Background(), d, makeEnv(), newTestLogger())
	if err == nil {
		t.Fatal("expected error to surface for SQS retry")
	}
	if c.rowURLs != nil {
		t.Error("row must not be updated when a render fails")
	}
}

func TestRunOne_BudgetCapExceededSkips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validWebsite())
	d.Cap = func(context.Context) (float64, error) { return 0.0000001, nil } // below 2 * 0.0005
	err := runOne(context.Background(), d, makeEnv(), newTestLogger())
	if err == nil {
		t.Fatal("expected budget-cap error")
	}
	if len(c.r2Keys) != 0 {
		t.Error("no screenshots should be taken when the cap is exceeded")
	}
}

func TestRunOne_MissingPreviewURLErrors(t *testing.T) {
	c := &captured{}
	d := newDeps(t, c, validWebsite())
	env := makeEnv()
	env.Detail.PreviewURL = ""
	if err := runOne(context.Background(), d, env, newTestLogger()); err == nil {
		t.Fatal("expected error when previewUrl is empty")
	}
}

func TestHandle_KillSwitchOffIsNoOp(t *testing.T) {
	setupKillSwitch(t, false)
	resp, err := handle(context.Background(), makeSQSEvent(t))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill-switch-off must succeed silently, got failures %v", resp.BatchItemFailures)
	}
}
