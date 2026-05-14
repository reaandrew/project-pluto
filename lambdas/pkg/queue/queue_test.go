package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

type fakeDDB struct {
	awaitingCount  int
	qualifiedCount int
	err            error
	lastQueries    []*dynamodb.QueryInput
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
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

func (f *fakeDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.lastQueries = append(f.lastQueries, in)
	if f.err != nil {
		return nil, f.err
	}
	pkAttr := in.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
	var count int
	switch {
	case strings.Contains(pkAttr, AwaitingReviewStatus):
		count = f.awaitingCount
	case strings.Contains(pkAttr, QualifiedStatus):
		count = f.qualifiedCount
	}
	return &dynamodb.QueryOutput{Count: int32(count)}, nil
}

func setupDDB(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func TestCountActiveSlots_SumsTwoPartitions(t *testing.T) {
	f := setupDDB(t)
	f.awaitingCount = 3
	f.qualifiedCount = 4

	got, err := CountActiveSlots(context.Background())
	if err != nil {
		t.Fatalf("CountActiveSlots: %v", err)
	}
	if got != 7 {
		t.Errorf("expected 3+4=7, got %d", got)
	}
}

func TestCountActiveSlots_FilterExpressionUsedForQualifiedOnly(t *testing.T) {
	f := setupDDB(t)
	if _, err := CountActiveSlots(context.Background()); err != nil {
		t.Fatalf("CountActiveSlots: %v", err)
	}
	if len(f.lastQueries) != 2 {
		t.Fatalf("expected 2 queries, got %d", len(f.lastQueries))
	}
	// First query = awaiting_review, no filter.
	awaiting := f.lastQueries[0]
	if awaiting.FilterExpression != nil {
		t.Errorf("awaiting_review query should have no filter; got %q", *awaiting.FilterExpression)
	}
	// Second query = qualified, with awaitingPromotion filter.
	qual := f.lastQueries[1]
	if qual.FilterExpression == nil || !strings.Contains(*qual.FilterExpression, "awaitingPromotion") {
		t.Errorf("qualified query should filter awaitingPromotion; got %v", qual.FilterExpression)
	}
}

func TestCountActiveSlots_PaginationSummed(t *testing.T) {
	// Build a fake that returns 2 pages on the second (qualified) query.
	calls := 0
	f := &paginatingFakeDDB{
		queryFn: func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			calls++
			pkAttr := in.ExpressionAttributeValues[":pk"].(*dtypes.AttributeValueMemberS).Value
			if strings.Contains(pkAttr, QualifiedStatus) {
				if in.ExclusiveStartKey == nil {
					return &dynamodb.QueryOutput{
						Count:            5,
						LastEvaluatedKey: map[string]dtypes.AttributeValue{"pk": &dtypes.AttributeValueMemberS{Value: "next"}},
					}, nil
				}
				return &dynamodb.QueryOutput{Count: 3}, nil
			}
			return &dynamodb.QueryOutput{Count: 1}, nil
		},
	}
	t.Setenv("ITEMS_TABLE", "items-test")
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })

	got, err := CountActiveSlots(context.Background())
	if err != nil {
		t.Fatalf("CountActiveSlots: %v", err)
	}
	if got != 1+5+3 {
		t.Errorf("expected paginated sum 9, got %d", got)
	}
	if calls != 3 {
		t.Errorf("expected 3 Query calls (1 awaiting + 2 qualified pages), got %d", calls)
	}
}

func TestCountActiveSlots_ErrorSurfaces(t *testing.T) {
	f := setupDDB(t)
	f.err = errors.New("ddb down")
	if _, err := CountActiveSlots(context.Background()); err == nil {
		t.Fatal("expected error to surface")
	}
}

func TestEncodeGSI1SK(t *testing.T) {
	cases := map[float64]string{
		0.0:    "0.0000#abc",
		0.55:   "0.5500#abc",
		0.8523: "0.8523#abc",
		1.0:    "1.0000#abc",
	}
	for in, want := range cases {
		got := EncodeGSI1SK(in, "abc")
		if got != want {
			t.Errorf("EncodeGSI1SK(%.4f) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeGSI1SK_LexicalSortMatchesNumeric(t *testing.T) {
	// gsi1sk strings must lex-sort the same as priorityScore numeric
	// order — that's what makes ScanIndexForward=false return
	// highest-priority-first without an in-Lambda sort.
	scores := []float64{0.05, 0.5, 0.85, 0.999, 0.1}
	encoded := make([]string, len(scores))
	for i, s := range scores {
		encoded[i] = EncodeGSI1SK(s, "x")
	}
	// Sort encoded[] lexically should match a numeric-desc sort of scores[].
	// Use the simpler "encoded[i] < encoded[j] iff scores[i] < scores[j]"
	// invariant pairwise.
	for i := 0; i < len(scores); i++ {
		for j := i + 1; j < len(scores); j++ {
			if (scores[i] < scores[j]) != (encoded[i] < encoded[j]) {
				t.Errorf("lex-sort mismatch: %.4f→%q vs %.4f→%q",
					scores[i], encoded[i], scores[j], encoded[j])
			}
		}
	}
}

// --- helpers -------------------------------------------------------------

type paginatingFakeDDB struct {
	queryFn func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
}

func (f *paginatingFakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (f *paginatingFakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (f *paginatingFakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *paginatingFakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *paginatingFakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *paginatingFakeDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return f.queryFn(in)
}
