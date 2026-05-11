package idempotency

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
	calls    int
	gotInput *dynamodb.PutItemInput
	// returnErr is what PutItem returns; nil = success
	returnErr error
	// fail indicates the conditional check failure should be returned
	conditionalFailure bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.calls++
	f.gotInput = in
	if f.conditionalFailure {
		return nil, &dtypes.ConditionalCheckFailedException{Message: aws.String("condition failed")}
	}
	if f.returnErr != nil {
		return nil, f.returnErr
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

// withFakeClient sets up a fake DDB client and the ITEMS_TABLE env var for the
// duration of the test. Returns the fake so tests can inspect call counts.
func withFakeClient(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return fake
}

func TestKeyShapeIncludesConsumerAndEventID(t *testing.T) {
	if got, want := Key("audit", "evt-1"), "IDEMP#audit#evt-1"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
}

func TestWithIdempotencyFirstCallRunsFn(t *testing.T) {
	fake := withFakeClient(t)

	called := 0
	got, err := WithIdempotency(context.Background(), "audit", "evt-1", func(_ context.Context) (string, error) {
		called++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("WithIdempotency: %v", err)
	}
	if got != "ok" {
		t.Errorf("returned %q, want %q", got, "ok")
	}
	if called != 1 {
		t.Errorf("fn called %d times, want 1", called)
	}
	if fake.calls != 1 {
		t.Errorf("PutItem called %d times, want 1", fake.calls)
	}

	if fake.gotInput == nil {
		t.Fatal("no PutItem call captured")
	}
	if aws.ToString(fake.gotInput.TableName) != "items-test" {
		t.Errorf("table = %q", aws.ToString(fake.gotInput.TableName))
	}
	if cond := aws.ToString(fake.gotInput.ConditionExpression); cond != "attribute_not_exists(pk)" {
		t.Errorf("ConditionExpression = %q", cond)
	}
	if pk := fake.gotInput.Item["pk"].(*dtypes.AttributeValueMemberS).Value; pk != "IDEMP#audit#evt-1" {
		t.Errorf("pk = %q", pk)
	}
	if tp := fake.gotInput.Item["type"].(*dtypes.AttributeValueMemberS).Value; tp != RecordType {
		t.Errorf("type = %q", tp)
	}
}

func TestWithIdempotencyReplayReturnsErrAlreadyProcessed(t *testing.T) {
	fake := withFakeClient(t)
	fake.conditionalFailure = true

	called := 0
	_, err := WithIdempotency(context.Background(), "audit", "evt-1", func(_ context.Context) (struct{}, error) {
		called++
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrAlreadyProcessed) {
		t.Fatalf("err = %v, want ErrAlreadyProcessed", err)
	}
	if called != 0 {
		t.Errorf("fn called %d times on replay, want 0", called)
	}
}

func TestWithIdempotencyTwoConsumersBothProcessSameEvent(t *testing.T) {
	fake := withFakeClient(t)

	called := 0
	for _, consumer := range []string{"audit", "qualifier"} {
		_, err := WithIdempotency(context.Background(), consumer, "evt-1", func(_ context.Context) (struct{}, error) {
			called++
			return struct{}{}, nil
		})
		if err != nil {
			t.Fatalf("WithIdempotency for %s: %v", consumer, err)
		}
	}
	if called != 2 {
		t.Errorf("fn called %d times, want 2 (one per consumer)", called)
	}
	if fake.calls != 2 {
		t.Errorf("PutItem called %d times, want 2", fake.calls)
	}
}

func TestWithIdempotencyPropagatesFnError(t *testing.T) {
	withFakeClient(t)

	wantErr := errors.New("fn failed")
	_, err := WithIdempotency(context.Background(), "audit", "evt-1", func(_ context.Context) (struct{}, error) {
		return struct{}{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestWithIdempotencyPropagatesPutItemError(t *testing.T) {
	fake := withFakeClient(t)
	wantErr := errors.New("network down")
	fake.returnErr = wantErr

	called := 0
	_, err := WithIdempotency(context.Background(), "audit", "evt-1", func(_ context.Context) (struct{}, error) {
		called++
		return struct{}{}, nil
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v wrapped", err, wantErr)
	}
	if called != 0 {
		t.Errorf("fn called %d times when PutItem failed, want 0", called)
	}
}

func TestWithIdempotencyRequiresConsumerAndEventID(t *testing.T) {
	withFakeClient(t)

	if _, err := WithIdempotency(context.Background(), "", "evt-1", noop); err == nil {
		t.Error("expected error for empty consumer")
	}
	if _, err := WithIdempotency(context.Background(), "audit", "", noop); err == nil {
		t.Error("expected error for empty eventID")
	}
}

func TestWithIdempotencyRequiresItemsTable(t *testing.T) {
	t.Setenv("ITEMS_TABLE", "")
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() { ddb.SetClient(nil) })

	_, err := WithIdempotency(context.Background(), "audit", "evt-1", noop)
	if err == nil || !strings.Contains(err.Error(), "ITEMS_TABLE") {
		t.Errorf("expected ITEMS_TABLE error, got %v", err)
	}
}

func TestWithIdempotencyTTLSetToDefault(t *testing.T) {
	fake := withFakeClient(t)

	frozen := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	prev := nowFunc
	nowFunc = func() time.Time { return frozen }
	t.Cleanup(func() { nowFunc = prev })

	_, err := WithIdempotency(context.Background(), "audit", "evt-1", noop)
	if err != nil {
		t.Fatalf("WithIdempotency: %v", err)
	}
	expVal := fake.gotInput.Item["expires_at"].(*dtypes.AttributeValueMemberN).Value
	wantTS := frozen.Add(DefaultTTL).Unix()
	if expVal != itoa(wantTS) {
		t.Errorf("expires_at = %s, want %d (now + 24h)", expVal, wantTS)
	}
}

func noop(_ context.Context) (struct{}, error) { return struct{}{}, nil }

// itoa avoids importing strconv just for one int64.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
