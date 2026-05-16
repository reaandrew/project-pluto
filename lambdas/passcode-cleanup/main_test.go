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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const fixedNow = "2026-05-16T12:00:00Z"

func depsWith(scan func(context.Context, int64) ([]websiteRef, error),
	wipe func(context.Context, websiteRef, string) (bool, error),
	pub func(context.Context, WipedDetail) error) runDeps {
	return runDeps{
		ScanDue: scan,
		Wipe:    wipe,
		Publish: pub,
		Now:     func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) },
	}
}

func TestSweep_WipesAndPublishes(t *testing.T) {
	var published []WipedDetail
	d := depsWith(
		func(context.Context, int64) ([]websiteRef, error) {
			return []websiteRef{
				{BusinessID: "biz-1", WebsiteID: "web-1", Reason: "sent_24h"},
				{BusinessID: "biz-2", WebsiteID: "web-2", Reason: "revealable_expired"},
			}, nil
		},
		func(context.Context, websiteRef, string) (bool, error) { return true, nil },
		func(_ context.Context, w WipedDetail) error { published = append(published, w); return nil },
	)
	if err := sweep(context.Background(), d, discardLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(published) != 2 {
		t.Fatalf("want 2 wipe events, got %d", len(published))
	}
	if published[0].BusinessID != "biz-1" || published[0].WebsiteID != "web-1" ||
		published[0].Reason != "sent_24h" || published[0].WipedAt != fixedNow {
		t.Errorf("event drift: %+v", published[0])
	}
	if published[1].Reason != "revealable_expired" {
		t.Errorf("8.6 reason must pass through: %+v", published[1])
	}
	// Privacy: the event payload must carry ids only — no cleartext-ish key.
	b, _ := json.Marshal(published[0])
	for _, banned := range []string{"cipher", "passcode", "cleartext", "hash"} {
		if strings.Contains(strings.ToLower(string(b)), banned) {
			t.Fatalf("event leaked %q: %s", banned, b)
		}
	}
}

func TestSweep_AlreadyWiped_NoPublish(t *testing.T) {
	pubCalled := false
	d := depsWith(
		func(context.Context, int64) ([]websiteRef, error) {
			return []websiteRef{{BusinessID: "b", WebsiteID: "w", Reason: "sent_24h"}}, nil
		},
		func(context.Context, websiteRef, string) (bool, error) { return false, nil }, // CCFE → already gone
		func(context.Context, WipedDetail) error { pubCalled = true; return nil },
	)
	if err := sweep(context.Background(), d, discardLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if pubCalled {
		t.Error("no event must be published when nothing was wiped")
	}
}

func TestSweep_WipeError_ContinuesAndReturnsErr(t *testing.T) {
	var published []WipedDetail
	d := depsWith(
		func(context.Context, int64) ([]websiteRef, error) {
			return []websiteRef{
				{BusinessID: "bad", WebsiteID: "w1", Reason: "sent_24h"},
				{BusinessID: "good", WebsiteID: "w2", Reason: "sent_24h"},
			}, nil
		},
		func(_ context.Context, r websiteRef, _ string) (bool, error) {
			if r.BusinessID == "bad" {
				return false, errors.New("throttled")
			}
			return true, nil
		},
		func(_ context.Context, w WipedDetail) error { published = append(published, w); return nil },
	)
	err := sweep(context.Background(), d, discardLogger())
	if err == nil {
		t.Fatal("a failed wipe must surface as a sweep error (so the schedule retries)")
	}
	if len(published) != 1 || published[0].BusinessID != "good" {
		t.Errorf("the healthy item must still be processed: %+v", published)
	}
}

func TestSweep_ScanError(t *testing.T) {
	d := depsWith(
		func(context.Context, int64) ([]websiteRef, error) { return nil, errors.New("scan boom") },
		func(context.Context, websiteRef, string) (bool, error) { return true, nil },
		func(context.Context, WipedDetail) error { return nil },
	)
	if err := sweep(context.Background(), d, discardLogger()); err == nil {
		t.Fatal("scan failure must abort the sweep")
	}
}

// fakeDDB drives scanDue: page 1 (with LastEvaluatedKey) then page 2.
type fakeDDB struct{ calls int }

func (f *fakeDDB) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	// Privacy + correctness: must project keys + the two non-secret
	// timestamp markers ONLY (never passcodeCipher), and filter on the
	// cipher's existence AND either trigger.
	if in.ProjectionExpression == nil || *in.ProjectionExpression != "pk, sk, passcodeCleanupDueAt, passcodeRevealableUntil" {
		return nil, errors.New("scan must project keys + timestamps only (never passcodeCipher): " + aws.ToString(in.ProjectionExpression))
	}
	if strings.Contains(*in.ProjectionExpression, "passcodeCipher") {
		return nil, errors.New("projection must NEVER include passcodeCipher")
	}
	if !strings.Contains(*in.FilterExpression, "attribute_exists(passcodeCipher)") ||
		!strings.Contains(*in.FilterExpression, "passcodeCleanupDueAt <= :now OR passcodeRevealableUntil <= :now") {
		return nil, errors.New("unexpected filter: " + *in.FilterExpression)
	}
	f.calls++
	if f.calls == 1 {
		// Emailed: cleanup-due passed → sent_24h.
		return &dynamodb.ScanOutput{
			Items: []map[string]dtypes.AttributeValue{{
				"pk":                   &dtypes.AttributeValueMemberS{Value: "BUSINESS#b1"},
				"sk":                   &dtypes.AttributeValueMemberS{Value: "WEBSITE#w1"},
				"passcodeCleanupDueAt": &dtypes.AttributeValueMemberN{Value: "1000"},
			}},
			LastEvaluatedKey: map[string]dtypes.AttributeValue{
				"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#b1"},
			},
		}, nil
	}
	// Never emailed: only the revealable backstop fired → revealable_expired.
	return &dynamodb.ScanOutput{
		Items: []map[string]dtypes.AttributeValue{{
			"pk":                      &dtypes.AttributeValueMemberS{Value: "BUSINESS#b2"},
			"sk":                      &dtypes.AttributeValueMemberS{Value: "WEBSITE#w2"},
			"passcodeRevealableUntil": &dtypes.AttributeValueMemberN{Value: "1000"},
		}},
	}, nil
}
func (f *fakeDDB) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func TestScanDue_ParsesKeysAndPaginates(t *testing.T) {
	t.Setenv("ITEMS_TABLE", "items-test")
	ddb.SetClient(&fakeDDB{})
	t.Cleanup(func() { ddb.SetClient(nil) })

	refs, err := scanDue(context.Background(), 5000) // now=5000 > both markers (1000)
	if err != nil {
		t.Fatalf("scanDue: %v", err)
	}
	want := []websiteRef{
		{BusinessID: "b1", WebsiteID: "w1", Reason: "sent_24h"},
		{BusinessID: "b2", WebsiteID: "w2", Reason: "revealable_expired"},
	}
	if len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("refs drift (pagination/parse/reason): %+v", refs)
	}
}
