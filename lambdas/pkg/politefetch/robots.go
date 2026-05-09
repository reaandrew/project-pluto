package politefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/temoto/robotstxt"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// robotsTTL is how long a cached robots.txt stays fresh before re-fetch.
const robotsTTL = 24 * time.Hour

// robotsCache fetches and caches per-host robots.txt. Cache rows live in the
// items table at pk=ROBOTS#<host>, sk=DATA, expires_at=<+24h>. An in-process
// memo cache short-circuits repeated calls in the same warm Lambda container.
type robotsCache struct {
	userAgent string
	http      *http.Client
	now       func() time.Time

	mu  sync.Mutex
	mem map[string]*robotsMemo // host → parsed data
}

// robotsMemo carries the parsed robots.txt + the bot's group + when the entry
// expires.
type robotsMemo struct {
	data    *robotstxt.RobotsData
	group   *robotstxt.Group
	expires time.Time
}

// robotsRecord is the items-table shape.
type robotsRecord struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Type      string `dynamodbav:"type"`
	Host      string `dynamodbav:"host"`
	Body      string `dynamodbav:"body"`
	FetchedAt string `dynamodbav:"fetchedAt"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

// Allowed reports whether the bot may fetch u. Returns the Crawl-delay the
// host requests (zero if none). The robots.txt is fetched from the same
// scheme as u (production = https; tests use http via httptest).
func (r *robotsCache) Allowed(ctx context.Context, u *url.URL) (bool, time.Duration, error) {
	memo, err := r.lookup(ctx, u)
	if err != nil {
		return false, 0, err
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	allowed := memo.group.Test(path)
	delay := memo.group.CrawlDelay
	return allowed, delay, nil
}

func (r *robotsCache) lookup(ctx context.Context, u *url.URL) (*robotsMemo, error) {
	host := u.Host
	r.mu.Lock()
	if r.mem == nil {
		r.mem = make(map[string]*robotsMemo)
	}
	if m, ok := r.mem[host]; ok && r.now().Before(m.expires) {
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()

	body, expires, err := r.loadOrFetch(ctx, u)
	if err != nil {
		return nil, err
	}

	data, parseErr := robotstxt.FromBytes(body)
	if parseErr != nil {
		return nil, fmt.Errorf("politefetch: parsing robots.txt for %s: %w", host, parseErr)
	}
	memo := &robotsMemo{
		data:    data,
		group:   data.FindGroup(r.userAgent),
		expires: expires,
	}

	r.mu.Lock()
	r.mem[host] = memo
	r.mu.Unlock()
	return memo, nil
}

// loadOrFetch returns the cached robots.txt body, falling back to a live
// fetch if the cache is missing or expired. A missing or 404 robots.txt is
// treated as "everything allowed" — that's the canonical interpretation.
func (r *robotsCache) loadOrFetch(ctx context.Context, u *url.URL) ([]byte, time.Time, error) {
	rec, ok, err := r.readDDB(ctx, u.Host)
	if err != nil {
		return nil, time.Time{}, err
	}
	if ok {
		expires := time.Unix(rec.ExpiresAt, 0).UTC()
		if r.now().Before(expires) {
			return []byte(rec.Body), expires, nil
		}
	}

	body, err := r.fetchLive(ctx, u)
	if err != nil {
		return nil, time.Time{}, err
	}
	expires := r.now().Add(robotsTTL)
	if writeErr := r.writeDDB(ctx, u.Host, body, expires); writeErr != nil {
		return nil, time.Time{}, writeErr
	}
	return body, expires, nil
}

func (r *robotsCache) fetchLive(ctx context.Context, u *url.URL) ([]byte, error) {
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("politefetch: building robots request: %w", err)
	}
	req.Header.Set("User-Agent", r.userAgent)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("politefetch: fetching robots.txt for %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	// Per RFC 9309 §2.3.1.4, missing robots.txt = everything allowed.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return []byte{}, nil
	}
	if resp.StatusCode >= 500 {
		// 5xx = treat as completely disallowed per RFC 9309 §2.3.1.4.
		// Encode that as a single-line `Disallow: /` so the parser refuses everything.
		return []byte("User-agent: *\nDisallow: /\n"), nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("politefetch: reading robots.txt for %s: %w", u.Host, err)
	}
	return body, nil
}

func (r *robotsCache) readDDB(ctx context.Context, host string) (robotsRecord, bool, error) {
	var rec robotsRecord
	dc, err := ddb.Client(ctx)
	if err != nil {
		return rec, false, fmt.Errorf("politefetch: ddb client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return rec, false, errors.New("politefetch: ITEMS_TABLE not set")
	}
	out, err := dc.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "ROBOTS#" + host},
			"sk": &dtypes.AttributeValueMemberS{Value: "DATA"},
		},
	})
	if err != nil {
		return rec, false, fmt.Errorf("politefetch: GetItem ROBOTS: %w", err)
	}
	if len(out.Item) == 0 {
		return rec, false, nil
	}
	if err := attributevalue.UnmarshalMap(out.Item, &rec); err != nil {
		return rec, false, fmt.Errorf("politefetch: unmarshalling robots record: %w", err)
	}
	return rec, true, nil
}

func (r *robotsCache) writeDDB(ctx context.Context, host string, body []byte, expires time.Time) error {
	dc, err := ddb.Client(ctx)
	if err != nil {
		return fmt.Errorf("politefetch: ddb client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return errors.New("politefetch: ITEMS_TABLE not set")
	}
	rec := robotsRecord{
		PK:        "ROBOTS#" + host,
		SK:        "DATA",
		Type:      "RobotsCache",
		Host:      host,
		Body:      string(body),
		FetchedAt: r.now().Format(time.RFC3339),
		ExpiresAt: expires.Unix(),
	}
	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		return fmt.Errorf("politefetch: marshalling robots record: %w", err)
	}
	_, err = dc.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("politefetch: PutItem ROBOTS: %w", err)
	}
	return nil
}
