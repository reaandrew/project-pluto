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
			return []websiteRef{{"biz-1", "web-1"}, {"biz-2", "web-2"}}, nil
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
		func(context.Context, int64) ([]websiteRef, error) { return []websiteRef{{"b", "w"}}, nil },
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
			return []websiteRef{{"bad", "w1"}, {"good", "w2"}}, nil
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
	// Privacy + correctness: must project keys only and filter on the
	// cipher's existence + the due time.
	if in.ProjectionExpression == nil || *in.ProjectionExpression != "pk, sk" {
		return nil, errors.New("scan must project pk, sk only (never passcodeCipher)")
	}
	if !strings.Contains(*in.FilterExpression, "attribute_exists(passcodeCipher)") ||
		!strings.Contains(*in.FilterExpression, "passcodeCleanupDueAt <= :now") {
		return nil, errors.New("unexpected filter: " + *in.FilterExpression)
	}
	f.calls++
	if f.calls == 1 {
		return &dynamodb.ScanOutput{
			Items: []map[string]dtypes.AttributeValue{{
				"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#b1"},
				"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#w1"},
			}},
			LastEvaluatedKey: map[string]dtypes.AttributeValue{
				"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#b1"},
			},
		}, nil
	}
	return &dynamodb.ScanOutput{
		Items: []map[string]dtypes.AttributeValue{{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#b2"},
			"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#w2"},
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

	refs, err := scanDue(context.Background(), time.Now().Unix())
	if err != nil {
		t.Fatalf("scanDue: %v", err)
	}
	if len(refs) != 2 || refs[0] != (websiteRef{"b1", "w1"}) || refs[1] != (websiteRef{"b2", "w2"}) {
		t.Fatalf("refs drift (pagination/parse): %+v", refs)
	}
}
