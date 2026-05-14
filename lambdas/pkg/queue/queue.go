// Package queue exposes helpers around the review-queue cap from
// .ralph/specs/05-capacity-and-cost.md § "Review queue cap".
//
// The cap is on the count of "active" review slots — items the operator
// is currently being asked to look at OR items already qualified and
// waiting for preview generation. When the active count reaches
// `PipelineSettings.Caps.MaxReviewQueueSize`, new qualifying businesses
// are parked in the backlog (`awaitingPromotion=true`) and the
// backlog-promoter Lambda (iter 3.3) promotes them as slots open up.
//
// Iter 3.3 ships the helper + the qualifier wiring + the promoter
// Lambda; the events that actually free slots (`queue.slot.freed`)
// land in iter 6.x alongside the operator approve/reject UI.
package queue

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// AwaitingReviewStatus is the Business.status value used while the
// operator is reviewing a generated preview. iter 6.x sets it from
// the publisher's success path.
const AwaitingReviewStatus = "awaiting_review"

// QualifiedStatus is the Business.status the qualifier sets when a
// business clears MinQualificationScore.
const QualifiedStatus = "qualified"

// CountActiveSlots returns the count of Business rows currently
// occupying a review-queue slot. "Active" = (a) status=awaiting_review,
// OR (b) status=qualified AND awaitingPromotion is not true (i.e.
// queued for preview generation but not yet picked up).
//
// Two gsi1 Queries are needed — one per status partition. The
// awaitingPromotion filter is applied via FilterExpression so we
// don't have to maintain a third gsi just for the boolean flag.
//
// Returns the sum across both partitions. Errors from either query
// surface to the caller; the qualifier treats them as DLQ-worthy
// because mis-counting can either falsely backlog OR falsely
// promote, both costly.
func CountActiveSlots(ctx context.Context) (int, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return 0, err
	}
	table := ddb.TableName()
	if table == "" {
		return 0, fmt.Errorf("queue: ITEMS_TABLE is not set")
	}

	awaiting, err := countByStatus(ctx, client, table, AwaitingReviewStatus, false)
	if err != nil {
		return 0, fmt.Errorf("count awaiting_review: %w", err)
	}
	qualified, err := countByStatus(ctx, client, table, QualifiedStatus, true)
	if err != nil {
		return 0, fmt.Errorf("count qualified-not-backlogged: %w", err)
	}
	return awaiting + qualified, nil
}

// countByStatus runs a gsi1 Query for the given status partition. When
// excludeBacklogged is true, results with awaitingPromotion=true are
// filtered out client-side via FilterExpression — DDB still scans them
// in the partition (FilterExpression runs server-side post-key-match)
// but they don't count.
//
// Uses Select=COUNT so the response is just the integer count, not
// the items. Pagination is handled — large partitions return Count
// across pages.
func countByStatus(ctx context.Context, client ddb.API, table, status string, excludeBacklogged bool) (int, error) {
	in := &dynamodb.QueryInput{
		TableName:              aws.String(table),
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		Select:                 dtypes.SelectCount,
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
		},
	}
	if excludeBacklogged {
		// attribute_not_exists handles the legacy "field never set"
		// case; the equality term catches rows explicitly set to false.
		in.FilterExpression = aws.String(
			"attribute_not_exists(awaitingPromotion) OR awaitingPromotion = :false")
		in.ExpressionAttributeValues[":false"] = &dtypes.AttributeValueMemberBOOL{Value: false}
	}
	total := 0
	for {
		out, err := client.Query(ctx, in)
		if err != nil {
			return 0, err
		}
		total += int(out.Count)
		if out.LastEvaluatedKey == nil {
			return total, nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// BacklogCandidate is the projection the promoter reads when picking
// the highest-priority backlog entry. The full Business row isn't
// needed for the decision — only the priority encoded in gsi1sk.
type BacklogCandidate struct {
	BusinessID    string  `dynamodbav:"id"`
	PriorityScore float64 `dynamodbav:"-"` // filled by parsing gsi1sk
}

// EncodeGSI1SK returns the canonical sort-key format used by the
// qualifier when it transitions a Business to `qualified`. Four
// fractional digits + the businessID guarantee fixed-width lexical
// sorting that matches numeric priority order; ScanIndexForward=false
// gives highest-priority-first.
func EncodeGSI1SK(priorityScore float64, businessID string) string {
	return fmt.Sprintf("%.4f#%s", priorityScore, businessID)
}
