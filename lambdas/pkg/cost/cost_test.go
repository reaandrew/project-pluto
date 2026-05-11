package cost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

type fakeDDB struct {
	getOut      *dynamodb.GetItemOutput
	getErr      error
	gotGetInput *dynamodb.GetItemInput
	updateErr   error
	gotUpdateIn *dynamodb.UpdateItemInput
	updateCalls int
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.gotGetInput = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updateCalls++
	f.gotUpdateIn = in
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}

func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return fake
}

func freezeClock(t *testing.T) time.Time {
	t.Helper()
	frozen := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	prev := nowFunc
	nowFunc = func() time.Time { return frozen }
	t.Cleanup(func() { nowFunc = prev })
	return frozen
}

// --- Keys -----------------------------------------------------------------

func TestPKForDateAndSKForStage(t *testing.T) {
	d := time.Date(2026, 5, 9, 23, 59, 0, 0, time.UTC)
	if got, want := PKForDate(d), "CAP#2026-05-09"; got != want {
		t.Errorf("PKForDate = %q, want %q", got, want)
	}
	if got, want := SKForStage("audit"), "STAGE#audit"; got != want {
		t.Errorf("SKForStage = %q, want %q", got, want)
	}
}

// --- Get ------------------------------------------------------------------

func TestGetReturnsZeroWhenNoRecord(t *testing.T) {
	setup(t)
	frozen := freezeClock(t)

	rec, err := Get(context.Background(), "audit")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.SpentUsd != 0 || rec.CallCount != 0 {
		t.Errorf("expected zero spend, got %+v", rec)
	}
	if rec.Date != frozen.Format("2006-01-02") {
		t.Errorf("Date = %q, want today", rec.Date)
	}
}

func TestGetUnmarshalsExistingRecord(t *testing.T) {
	fake := setup(t)
	freezeClock(t)
	fake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":        &dtypes.AttributeValueMemberS{Value: "CAP#2026-05-09"},
			"sk":        &dtypes.AttributeValueMemberS{Value: "STAGE#audit"},
			"type":      &dtypes.AttributeValueMemberS{Value: RecordType},
			"stage":     &dtypes.AttributeValueMemberS{Value: "audit"},
			"date":      &dtypes.AttributeValueMemberS{Value: "2026-05-09"},
			"spentUsd":  &dtypes.AttributeValueMemberN{Value: "0.500000"},
			"callCount": &dtypes.AttributeValueMemberN{Value: "42"},
		},
	}
	rec, err := Get(context.Background(), "audit")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.SpentUsd != 0.5 || rec.CallCount != 42 || rec.Stage != "audit" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

func TestGetRequiresStage(t *testing.T) {
	setup(t)
	if _, err := Get(context.Background(), ""); err == nil {
		t.Error("expected error for empty stage")
	}
}

func TestGetRequiresItemsTable(t *testing.T) {
	t.Setenv("ITEMS_TABLE", "")
	ddb.SetClient(&fakeDDB{})
	t.Cleanup(func() { ddb.SetClient(nil) })

	if _, err := Get(context.Background(), "audit"); err == nil || !strings.Contains(err.Error(), "ITEMS_TABLE") {
		t.Errorf("expected ITEMS_TABLE error, got %v", err)
	}
}

func TestGetWrapsSDKError(t *testing.T) {
	fake := setup(t)
	wantErr := errors.New("boom")
	fake.getErr = wantErr
	if _, err := Get(context.Background(), "audit"); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrap of %v", err, wantErr)
	}
}

// --- Record ---------------------------------------------------------------

func TestRecordIssuesAtomicAdd(t *testing.T) {
	fake := setup(t)
	freezeClock(t)

	if err := Record(context.Background(), "audit", 0.012); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if fake.updateCalls != 1 {
		t.Fatalf("UpdateItem called %d times", fake.updateCalls)
	}
	in := fake.gotUpdateIn
	if aws.ToString(in.TableName) != "items-test" {
		t.Errorf("TableName = %q", aws.ToString(in.TableName))
	}
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	if pk != "CAP#2026-05-09" {
		t.Errorf("pk = %q", pk)
	}
	sk := in.Key["sk"].(*dtypes.AttributeValueMemberS).Value
	if sk != "STAGE#audit" {
		t.Errorf("sk = %q", sk)
	}
	if !strings.Contains(aws.ToString(in.UpdateExpression), "ADD spentUsd :usd, callCount :one") {
		t.Errorf("UpdateExpression missing ADD: %q", aws.ToString(in.UpdateExpression))
	}
	usd := in.ExpressionAttributeValues[":usd"].(*dtypes.AttributeValueMemberN).Value
	if usd != "0.012000" {
		t.Errorf(":usd = %q, want 0.012000", usd)
	}
}

func TestRecordRejectsNegative(t *testing.T) {
	setup(t)
	if err := Record(context.Background(), "audit", -0.01); err == nil {
		t.Error("expected error for negative usd")
	}
}

func TestRecordRequiresStageAndTable(t *testing.T) {
	setup(t)
	if err := Record(context.Background(), "", 0.01); err == nil {
		t.Error("expected error for empty stage")
	}
	t.Setenv("ITEMS_TABLE", "")
	if err := Record(context.Background(), "audit", 0.01); err == nil || !strings.Contains(err.Error(), "ITEMS_TABLE") {
		t.Errorf("expected ITEMS_TABLE error, got %v", err)
	}
}

// --- Assert ---------------------------------------------------------------

func TestAssertNoCapDisabled(t *testing.T) {
	setup(t)
	if err := Assert(context.Background(), "audit", 100, 0); err != nil {
		t.Errorf("expected nil with capUsd=0, got %v", err)
	}
	if err := Assert(context.Background(), "audit", 100, -1); err != nil {
		t.Errorf("expected nil with negative cap, got %v", err)
	}
}

func TestAssertUnderCap(t *testing.T) {
	fake := setup(t)
	freezeClock(t)
	fake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":       &dtypes.AttributeValueMemberS{Value: "CAP#2026-05-09"},
			"sk":       &dtypes.AttributeValueMemberS{Value: "STAGE#audit"},
			"spentUsd": &dtypes.AttributeValueMemberN{Value: "1.000000"},
		},
	}
	if err := Assert(context.Background(), "audit", 0.5, 5.0); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestAssertExceedsCapReturnsErrBudgetCapExceeded(t *testing.T) {
	fake := setup(t)
	freezeClock(t)
	fake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":       &dtypes.AttributeValueMemberS{Value: "CAP#2026-05-09"},
			"sk":       &dtypes.AttributeValueMemberS{Value: "STAGE#audit"},
			"spentUsd": &dtypes.AttributeValueMemberN{Value: "4.500000"},
		},
	}
	err := Assert(context.Background(), "audit", 0.6, 5.0)
	if !errors.Is(err, ErrBudgetCapExceeded) {
		t.Errorf("err = %v, want ErrBudgetCapExceeded", err)
	}
}

func TestAssertRejectsNegativeEstimate(t *testing.T) {
	setup(t)
	if err := Assert(context.Background(), "audit", -1, 5); err == nil {
		t.Error("expected error for negative estimate")
	}
}

// --- WithCostCap ----------------------------------------------------------

func TestWithCostCapHappyPath(t *testing.T) {
	fake := setup(t)
	freezeClock(t)

	called := 0
	got, err := WithCostCap(context.Background(), "audit", 0.1, 5.0, func(_ context.Context) (string, float64, error) {
		called++
		return "ok", 0.087, nil
	})
	if err != nil {
		t.Fatalf("WithCostCap: %v", err)
	}
	if got != "ok" {
		t.Errorf("returned %q, want ok", got)
	}
	if called != 1 {
		t.Errorf("fn called %d times, want 1", called)
	}
	if fake.updateCalls != 1 {
		t.Fatalf("Record called %d times, want 1", fake.updateCalls)
	}
	usd := fake.gotUpdateIn.ExpressionAttributeValues[":usd"].(*dtypes.AttributeValueMemberN).Value
	if usd != "0.087000" {
		t.Errorf("recorded :usd = %q, want 0.087000 (actual not estimate)", usd)
	}
}

func TestWithCostCapAssertFailsWithoutCallingFn(t *testing.T) {
	fake := setup(t)
	freezeClock(t)
	fake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":       &dtypes.AttributeValueMemberS{Value: "CAP#2026-05-09"},
			"sk":       &dtypes.AttributeValueMemberS{Value: "STAGE#audit"},
			"spentUsd": &dtypes.AttributeValueMemberN{Value: "4.999000"},
		},
	}
	called := 0
	_, err := WithCostCap(context.Background(), "audit", 0.1, 5.0, func(_ context.Context) (struct{}, float64, error) {
		called++
		return struct{}{}, 0.087, nil
	})
	if !errors.Is(err, ErrBudgetCapExceeded) {
		t.Errorf("err = %v, want ErrBudgetCapExceeded", err)
	}
	if called != 0 {
		t.Errorf("fn called %d times when over cap, want 0", called)
	}
	if fake.updateCalls != 0 {
		t.Errorf("Record called %d times when capped, want 0", fake.updateCalls)
	}
}

func TestWithCostCapDoesNotRecordWhenFnFails(t *testing.T) {
	fake := setup(t)
	freezeClock(t)

	wantErr := errors.New("fn boom")
	_, err := WithCostCap(context.Background(), "audit", 0.1, 5.0, func(_ context.Context) (string, float64, error) {
		return "", 0.087, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if fake.updateCalls != 0 {
		t.Errorf("Record called %d times on fn-error, want 0", fake.updateCalls)
	}
}

func TestWithCostCapSurfacesRecordError(t *testing.T) {
	fake := setup(t)
	freezeClock(t)
	fake.updateErr = errors.New("ddb down")

	got, err := WithCostCap(context.Background(), "audit", 0.1, 5.0, func(_ context.Context) (string, float64, error) {
		return "fn-ok", 0.087, nil
	})
	if err == nil {
		t.Fatal("expected record-failure error")
	}
	if !strings.Contains(err.Error(), "recording spend") {
		t.Errorf("error should mention recording spend: %v", err)
	}
	if got != "fn-ok" {
		t.Errorf("result still returned: got %q, want fn-ok", got)
	}
}
