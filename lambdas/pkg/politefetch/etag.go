package politefetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// etagTTL caps how long a cached body lives before re-validation. The cache
// is keyed by URL hash so the items table can hold entries for many sites
// without colliding.
const etagTTL = 24 * time.Hour

// etagCache stores ETag + Last-Modified + body for previously-seen URLs so
// subsequent fetches can short-circuit on 304.
type etagCache struct {
	now func() time.Time
}

// etagEntry is what Get returns and Put accepts.
type etagEntry struct {
	ETag         string
	LastModified string
	Body         []byte
}

// etagRecord is the items-table shape.
type etagRecord struct {
	PK           string `dynamodbav:"pk"`
	SK           string `dynamodbav:"sk"`
	Type         string `dynamodbav:"type"`
	URL          string `dynamodbav:"url"`
	ETag         string `dynamodbav:"etag"`
	LastModified string `dynamodbav:"lastModified"`
	Body         string `dynamodbav:"body"`
	StoredAt     string `dynamodbav:"storedAt"`
	ExpiresAt    int64  `dynamodbav:"expires_at"`
}

// keyFor hashes the URL so the partition key length is bounded regardless of
// query-string size.
func etagKeyFor(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:])
}

// Get returns the cached entry for url, or (nil, false) if missing or expired.
// DynamoDB errors are swallowed (cache miss) — the cache is an optimisation,
// never the source of truth.
func (c *etagCache) Get(ctx context.Context, url string) (*etagEntry, bool) {
	dc, err := ddb.Client(ctx)
	if err != nil {
		return nil, false
	}
	table := ddb.TableName()
	if table == "" {
		return nil, false
	}
	out, err := dc.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "ETAG#" + etagKeyFor(url)},
			"sk": &dtypes.AttributeValueMemberS{Value: "DATA"},
		},
	})
	if err != nil || len(out.Item) == 0 {
		return nil, false
	}
	var rec etagRecord
	if err := attributevalue.UnmarshalMap(out.Item, &rec); err != nil {
		return nil, false
	}
	if c.now().Unix() >= rec.ExpiresAt {
		return nil, false
	}
	return &etagEntry{
		ETag:         rec.ETag,
		LastModified: rec.LastModified,
		Body:         []byte(rec.Body),
	}, true
}

// Put stores a fresh response in the ETag cache. Skips the write if the
// response carries no validators (no point — we'd never get a 304 next
// time). Errors are returned but callers may ignore them: the cache is a
// best-effort optimisation.
func (c *etagCache) Put(ctx context.Context, url string, header http.Header, body []byte) error {
	etag := header.Get("ETag")
	lastModified := header.Get("Last-Modified")
	if etag == "" && lastModified == "" {
		return nil
	}
	dc, err := ddb.Client(ctx)
	if err != nil {
		return fmt.Errorf("politefetch: ddb client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return errors.New("politefetch: ITEMS_TABLE not set")
	}
	now := c.now()
	rec := etagRecord{
		PK:           "ETAG#" + etagKeyFor(url),
		SK:           "DATA",
		Type:         "EtagCache",
		URL:          url,
		ETag:         etag,
		LastModified: lastModified,
		Body:         string(body),
		StoredAt:     now.Format(time.RFC3339),
		ExpiresAt:    now.Add(etagTTL).Unix(),
	}
	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		return fmt.Errorf("politefetch: marshalling etag record: %w", err)
	}
	_, err = dc.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("politefetch: PutItem ETAG: %w", err)
	}
	return nil
}
