package politefetch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// hostThrottle implements a fleet-wide per-host token bucket with floor
// `floor` between successive fetches. Conditional UpdateItem on
// pk=THROTTLE#<host>, sk=BUCKET, lastFetchAt:
//
//	SET lastFetchAt = :now CONDITION attribute_not_exists OR lastFetchAt <= :gate
//
// where :gate = nowUnix - floor. If the condition fails, sleep for the
// remaining wait, then retry once. Counter slop on retry is acceptable;
// the rule is "no more than one fetch per host per `floor`" with eventual
// consistency under fleet contention, which DynamoDB delivers.
type hostThrottle struct {
	floor time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// throttleTTL keeps the bucket row from accumulating forever. Stale buckets
// (host not contacted for 7d) get TTL'd.
const throttleTTL = 7 * 24 * time.Hour

// Wait blocks until the caller may make a request to host. Returns an error
// if the context expires before the throttle window opens.
func (h *hostThrottle) Wait(ctx context.Context, host string, floor time.Duration) error {
	if floor <= 0 {
		floor = h.floor
	}
	for {
		acquired, waitFor, err := h.tryAcquire(ctx, host, floor)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		if err := h.sleep(ctx, waitFor); err != nil {
			return err
		}
	}
}

// tryAcquire attempts to claim the next throttle slot for host. Returns
// (acquired=true, 0, nil) on success, (false, waitFor, nil) when another
// fetcher beat us, or an error if DynamoDB itself refused.
func (h *hostThrottle) tryAcquire(ctx context.Context, host string, floor time.Duration) (bool, time.Duration, error) {
	dc, err := ddb.Client(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("politefetch: ddb client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return false, 0, errors.New("politefetch: ITEMS_TABLE not set")
	}

	now := h.now()
	gate := now.Add(-floor).Unix()
	expires := now.Add(throttleTTL).Unix()

	_, err = dc.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "THROTTLE#" + host},
			"sk": &dtypes.AttributeValueMemberS{Value: "BUCKET"},
		},
		UpdateExpression: aws.String(
			"SET lastFetchAt = :now, " +
				"#t = :type, " +
				"host = :host, " +
				"expires_at = :exp",
		),
		ConditionExpression: aws.String("attribute_not_exists(lastFetchAt) OR lastFetchAt <= :gate"),
		ExpressionAttributeNames: map[string]string{
			"#t": "type",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":now":  &dtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
			":gate": &dtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", gate)},
			":type": &dtypes.AttributeValueMemberS{Value: "ThrottleBucket"},
			":host": &dtypes.AttributeValueMemberS{Value: host},
			":exp":  &dtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", expires)},
		},
		ReturnValues: dtypes.ReturnValueAllOld,
	})
	if err == nil {
		return true, 0, nil
	}

	var ccfe *dtypes.ConditionalCheckFailedException
	if errors.As(err, &ccfe) {
		// Another fetcher just beat us. Compute remaining wait from the
		// stored lastFetchAt (in the condition-failure response item).
		waitFor := h.estimateWait(ccfe, floor)
		return false, waitFor, nil
	}
	return false, 0, fmt.Errorf("politefetch: throttle UpdateItem: %w", err)
}

// estimateWait reads lastFetchAt from a ConditionalCheckFailedException item
// and returns how long the caller should sleep before retrying. Falls back
// to the floor if the SDK didn't surface the item.
func (h *hostThrottle) estimateWait(ccfe *dtypes.ConditionalCheckFailedException, floor time.Duration) time.Duration {
	if ccfe == nil || ccfe.Item == nil {
		return floor
	}
	last, ok := ccfe.Item["lastFetchAt"]
	if !ok {
		return floor
	}
	n, ok := last.(*dtypes.AttributeValueMemberN)
	if !ok {
		return floor
	}
	var lastUnix int64
	if _, err := fmt.Sscanf(n.Value, "%d", &lastUnix); err != nil {
		return floor
	}
	elapsed := h.now().Sub(time.Unix(lastUnix, 0))
	remaining := floor - elapsed
	if remaining < 0 {
		return 0
	}
	// Add a tiny slop so we don't immediately re-collide.
	return remaining + 50*time.Millisecond
}
