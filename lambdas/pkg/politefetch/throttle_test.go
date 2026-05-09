package politefetch

import (
	"context"
	"fmt"
	"testing"
	"time"

	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestThrottleAcquiresFirstSlotImmediately(t *testing.T) {
	fake := setup(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	h := &hostThrottle{
		floor: 5 * time.Second,
		now:   func() time.Time { return now },
		sleep: func(_ context.Context, _ time.Duration) error { return nil },
	}
	if err := h.Wait(context.Background(), "example.com", 0); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if fake.updateCalls != 1 {
		t.Errorf("UpdateItem called %d times, want 1", fake.updateCalls)
	}
}

func TestThrottleRetriesOnConditionalFailure(t *testing.T) {
	fake := setup(t)

	// Pre-seed lastFetchAt so estimateWait reads a value (3 seconds ago, floor 5s → wait 2s).
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	pk := "THROTTLE#example.com"
	sk := "BUCKET"
	fake.store[pk+"|"+sk] = map[string]dtypes.AttributeValue{
		"pk":          &dtypes.AttributeValueMemberS{Value: pk},
		"sk":          &dtypes.AttributeValueMemberS{Value: sk},
		"lastFetchAt": &dtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Add(-3*time.Second).Unix())},
	}
	fake.updateFailNTimes = 1

	var slept time.Duration
	h := &hostThrottle{
		floor: 5 * time.Second,
		now:   func() time.Time { return now },
		sleep: func(_ context.Context, d time.Duration) error {
			slept = d
			return nil
		},
	}
	if err := h.Wait(context.Background(), "example.com", 0); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if fake.updateCalls != 2 {
		t.Errorf("UpdateItem called %d times, want 2 (first failed, second succeeded)", fake.updateCalls)
	}
	// Should sleep ~ floor - elapsed = 5 - 3 = 2s (plus small slop).
	if slept < 2*time.Second || slept > 3*time.Second {
		t.Errorf("slept = %v, want ~2s", slept)
	}
}

func TestEstimateWaitFallsBackToFloor(t *testing.T) {
	h := &hostThrottle{
		floor: 5 * time.Second,
		now:   func() time.Time { return time.Now() },
	}
	// nil exception → fallback
	if got := h.estimateWait(nil, 5*time.Second); got != 5*time.Second {
		t.Errorf("nil ccfe: got %v, want floor", got)
	}
	// no Item → fallback
	got := h.estimateWait(&dtypes.ConditionalCheckFailedException{}, 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("nil item: got %v, want floor", got)
	}
	// missing lastFetchAt → fallback
	got = h.estimateWait(&dtypes.ConditionalCheckFailedException{Item: map[string]dtypes.AttributeValue{}}, 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("missing lastFetchAt: got %v, want floor", got)
	}
}

func TestThrottleRequiresItemsTable(t *testing.T) {
	setup(t)                    // sets ITEMS_TABLE to "items-test"
	t.Setenv("ITEMS_TABLE", "") // override blank for this test

	h := &hostThrottle{
		floor: 5 * time.Second,
		now:   func() time.Time { return time.Now() },
		sleep: func(_ context.Context, _ time.Duration) error { return nil },
	}
	if err := h.Wait(context.Background(), "example.com", 0); err == nil {
		t.Error("expected error when ITEMS_TABLE unset")
	}
}
