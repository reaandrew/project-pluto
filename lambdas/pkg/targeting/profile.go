// Package targeting models the TargetingProfile DynamoDB row and the
// CRUD operations the api-targeting Lambda + the discover Lambda
// (iter 1.3) share. Schema mirrors .ralph/specs/02-data-model.md §
// "TargetingProfile":
//
//	pk     = "TARGET#<id>"
//	sk     = "PROFILE"
//	gsi1pk = "TARGET#ENABLED"   (only set when enabled = true)
//	gsi1sk = "<vertical>#<location>"
//
// Stats counters (discovered7d, qualified7d, approved7d) are NOT
// updated through this package — they're populated by analytics
// elsewhere. CRUD callers should treat Stats as read-only.
package targeting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// Profile is the TargetingProfile shape. DDB-typed via the
// `dynamodbav` tag; JSON-typed via `json` for the HTTP API. The id
// field is the canonical identifier; pk/sk/gsi1pk/gsi1sk are derived
// from it on write and not exposed to API callers.
type Profile struct {
	ID              string   `json:"id"              dynamodbav:"id"`
	Vertical        string   `json:"vertical"        dynamodbav:"vertical"`
	Location        string   `json:"location"        dynamodbav:"location"`
	IncludeKeywords []string `json:"includeKeywords" dynamodbav:"includeKeywords"`
	ExcludeKeywords []string `json:"excludeKeywords" dynamodbav:"excludeKeywords"`
	Weights         Weights  `json:"weights"         dynamodbav:"weights"`
	Enabled         bool     `json:"enabled"         dynamodbav:"enabled"`
	LastRunAt       string   `json:"lastRunAt"       dynamodbav:"lastRunAt,omitempty"`
	Stats           Stats    `json:"stats"           dynamodbav:"stats"`
	CreatedAt       string   `json:"createdAt"       dynamodbav:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"       dynamodbav:"updatedAt"`
	Etag            string   `json:"etag"            dynamodbav:"etag"`
}

// Weights govern priorityScore. Sum must equal 1.0 (within float
// rounding); Validate enforces this so the qualifier (iter 3.1)
// can multiply directly.
type Weights struct {
	WebsiteAge        float64 `json:"websiteAge"        dynamodbav:"websiteAge"`
	AuditScore        float64 `json:"auditScore"        dynamodbav:"auditScore"`
	BusinessSize      float64 `json:"businessSize"      dynamodbav:"businessSize"`
	ContactConfidence float64 `json:"contactConfidence" dynamodbav:"contactConfidence"`
	VerticalFit       float64 `json:"verticalFit"       dynamodbav:"verticalFit"`
}

// Stats are read-only counters populated by analytics. Round-trip
// them on read/write so the row schema stays stable, but never
// trust API-supplied values — Reset() zeroes them on create/update
// so a malicious caller can't lie about historical performance.
type Stats struct {
	Discovered7d int `json:"discovered7d" dynamodbav:"discovered7d"`
	Qualified7d  int `json:"qualified7d"  dynamodbav:"qualified7d"`
	Approved7d   int `json:"approved7d"   dynamodbav:"approved7d"`
}

// Validate checks weights sum to 1.0 ±0.01 and that the required
// scalar fields are non-empty. Returns nil when the profile is
// valid; otherwise an error suitable for a 400 response body.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Vertical) == "" {
		return errors.New("vertical is required")
	}
	if strings.TrimSpace(p.Location) == "" {
		return errors.New("location is required")
	}
	sum := p.Weights.WebsiteAge + p.Weights.AuditScore +
		p.Weights.BusinessSize + p.Weights.ContactConfidence + p.Weights.VerticalFit
	if sum < 0.99 || sum > 1.01 {
		return fmt.Errorf("weights must sum to 1.0 (got %.4f)", sum)
	}
	for _, w := range []float64{
		p.Weights.WebsiteAge, p.Weights.AuditScore, p.Weights.BusinessSize,
		p.Weights.ContactConfidence, p.Weights.VerticalFit,
	} {
		if w < 0 {
			return errors.New("weights must be non-negative")
		}
	}
	return nil
}

// nowFunc is overridable in tests so timestamps + etags are
// deterministic.
var nowFunc = func() time.Time { return time.Now().UTC() }

// SetNowFunc replaces the time source. Test-only.
func SetNowFunc(f func() time.Time) { nowFunc = f }

// newIDFunc + newEtagFunc are overridable in tests too. The
// production paths generate random hex strings via crypto/rand.
var newIDFunc = randomHex
var newEtagFunc = randomHex

// SetIDFunc + SetEtagFunc are test-only overrides.
func SetIDFunc(f func(int) string)   { newIDFunc = f }
func SetEtagFunc(f func(int) string) { newEtagFunc = f }

// randomHex returns n random bytes hex-encoded. Used for ID + etag
// generation.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand reading from /dev/urandom shouldn't fail on
		// Linux; if it does, panic — the alternative is silent
		// duplicate IDs.
		panic(fmt.Errorf("targeting: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

// pkFor + skConst keep the key conventions in one place — every
// caller that handcrafts a key risks a typo.
const skConst = "PROFILE"

func pkFor(id string) string { return "TARGET#" + id }

// Create inserts a fresh row, generating id + etag + timestamps,
// zeroing stats, and writing the gsi1 keys when enabled. Returns
// the inserted Profile (with id/etag/timestamps populated).
//
// ErrInvalid (returned wrapped) means the body failed validation —
// callers should translate to 400.
func Create(ctx context.Context, p Profile) (Profile, error) {
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	now := nowFunc().Format(time.RFC3339)
	p.ID = newIDFunc(16)
	p.Etag = newEtagFunc(16)
	p.CreatedAt = now
	p.UpdatedAt = now
	p.Stats = Stats{} // API callers can't seed stats
	if err := putItem(ctx, p, ""); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Get reads the profile by id. Returns ErrNotFound (wrapped) when
// the row is absent.
func Get(ctx context.Context, id string) (Profile, error) {
	var p Profile
	if id == "" {
		return p, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	client, err := ddb.Client(ctx)
	if err != nil {
		return p, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pkFor(id)},
			"sk": &dtypes.AttributeValueMemberS{Value: skConst},
		},
	})
	if err != nil {
		return p, fmt.Errorf("targeting: GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return p, ErrNotFound
	}
	if err := attributevalue.UnmarshalMap(out.Item, &p); err != nil {
		return p, fmt.Errorf("targeting: unmarshal: %w", err)
	}
	return p, nil
}

// List returns every targeting profile in the table. Uses a Query
// on the items table's main partition would be wrong (no shared
// pk); a Scan with type=TargetingProfile filter is acceptable
// because the operator-facing list page tolerates the latency, and
// total profile count stays small (<100 across the project's life).
func List(ctx context.Context) ([]Profile, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(ddb.TableName()),
		FilterExpression: aws.String("#t = :type"),
		ExpressionAttributeNames: map[string]string{
			"#t": "type",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":type": &dtypes.AttributeValueMemberS{Value: "TargetingProfile"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("targeting: Scan: %w", err)
	}
	profiles := make([]Profile, 0, len(out.Items))
	for _, item := range out.Items {
		var p Profile
		if err := attributevalue.UnmarshalMap(item, &p); err != nil {
			return nil, fmt.Errorf("targeting: unmarshal scan row: %w", err)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// Update overwrites the row identified by id with the contents of
// p (after validation + etag check). The caller must supply the
// etag they saw on the last read; ErrEtagMismatch is returned when
// the row in the table has been updated since.
func Update(ctx context.Context, id string, p Profile, expectedEtag string) (Profile, error) {
	if id == "" {
		return Profile{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	if expectedEtag == "" {
		return Profile{}, fmt.Errorf("%w: expected etag is required", ErrInvalid)
	}
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	current, err := Get(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	// Preserve server-controlled fields the API caller can't lie
	// about: id, createdAt, stats.
	p.ID = current.ID
	p.CreatedAt = current.CreatedAt
	p.Stats = current.Stats
	p.UpdatedAt = nowFunc().Format(time.RFC3339)
	p.Etag = newEtagFunc(16)
	if err := putItem(ctx, p, expectedEtag); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Delete removes the row. ErrNotFound returned on a missing id.
func Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrInvalid)
	}
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	out, err := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pkFor(id)},
			"sk": &dtypes.AttributeValueMemberS{Value: skConst},
		},
		ReturnValues: dtypes.ReturnValueAllOld,
	})
	if err != nil {
		return fmt.Errorf("targeting: DeleteItem: %w", err)
	}
	if len(out.Attributes) == 0 {
		return ErrNotFound
	}
	return nil
}

// Sentinel errors. Callers use errors.Is to map to HTTP statuses.
var (
	ErrNotFound     = errors.New("targeting: profile not found")
	ErrInvalid      = errors.New("targeting: invalid profile")
	ErrEtagMismatch = errors.New("targeting: etag mismatch — refresh and retry")
)

// putItem writes the profile + the pk/sk + the type marker + the
// gsi1 keys (when enabled). Uses a ConditionExpression on the etag
// when expectedEtag is non-empty (i.e. Update path); the empty
// string is the Create path which has no precondition.
func putItem(ctx context.Context, p Profile, expectedEtag string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(p)
	if err != nil {
		return fmt.Errorf("targeting: marshal: %w", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: pkFor(p.ID)}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: skConst}
	item["type"] = &dtypes.AttributeValueMemberS{Value: "TargetingProfile"}
	if p.Enabled {
		item["gsi1pk"] = &dtypes.AttributeValueMemberS{Value: "TARGET#ENABLED"}
		item["gsi1sk"] = &dtypes.AttributeValueMemberS{Value: p.Vertical + "#" + p.Location}
	}
	in := &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}
	if expectedEtag != "" {
		in.ConditionExpression = aws.String("etag = :expected")
		in.ExpressionAttributeValues = map[string]dtypes.AttributeValue{
			":expected": &dtypes.AttributeValueMemberS{Value: expectedEtag},
		}
	}
	if _, err := client.PutItem(ctx, in); err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return ErrEtagMismatch
		}
		return fmt.Errorf("targeting: PutItem: %w", err)
	}
	return nil
}
