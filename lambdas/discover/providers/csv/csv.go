// Package csv implements the CSV discovery provider. The operator
// uploads a CSV to S3 at csv://<bucket>/<key>; this provider reads
// the object, parses it, and emits one DiscoveredBusiness per row.
//
// Used for backfills, partner lists, and manual research where the
// operator wants to feed a fixed list of businesses into the
// pipeline. Free (no per-call cost). The CSV's source-of-truth is
// the operator — we trust the file's contents but still apply the
// downstream audit/qualifier gates the same as for any other
// provider.
//
// Expected CSV columns (header row required):
//
//	name,domain,location,vertical
//
// Rows missing a `name` are skipped. `domain` may be empty (the
// audit Lambda will resolve it later). Extra columns are ignored —
// future migrations can introduce new fields without breaking
// existing uploads.
package csv

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

const providerID = "csv"

// S3Object is the subset of *s3.Client we need. Tests inject a fake
// to avoid reaching AWS.
type S3Object interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Provider reads CSV uploads from a known S3 bucket and yields one
// DiscoveredBusiness per parseable row.
type Provider struct {
	// Bucket holds operator-uploaded CSVs. Set at Lambda startup
	// from an env var or SSM lookup.
	Bucket string
	// Key is the specific object to read on this run. iter 1.3
	// will replace this with a strategy that lists pending uploads
	// and processes each in turn; for 1.2 the caller passes the
	// key explicitly.
	Key string
	// Client is overridable for tests. nil → lazy init via the
	// default credential chain.
	Client S3Object
}

func (p *Provider) ID() string              { return providerID }
func (p *Provider) CostPerCallUSD() float64 { return 0 }

// Find pulls the configured S3 object, parses it as CSV, and
// returns up to `cap` rows as DiscoveredBusiness records. Rows
// missing a name are skipped (counted in the error message if all
// rows are bad). When `cap <= 0`, no upper bound is applied.
func (p *Provider) Find(ctx context.Context, _ discovery.FindRequest, cap int) ([]discovery.DiscoveredBusiness, error) {
	if p.Bucket == "" || p.Key == "" {
		return nil, fmt.Errorf("csv: bucket and key are required")
	}
	client, err := p.resolveClient(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.Bucket),
		Key:    aws.String(p.Key),
	})
	if err != nil {
		return nil, fmt.Errorf("csv: GetObject %s/%s: %w", p.Bucket, p.Key, err)
	}
	defer func() { _ = out.Body.Close() }()
	return parseCSV(out.Body, cap)
}

// resolveClient lazily constructs the S3 client when one wasn't
// injected. Mirrors the pattern in pkg/ddb.
func (p *Provider) resolveClient(ctx context.Context) (S3Object, error) {
	if p.Client != nil {
		return p.Client, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("csv: loading AWS config: %w", err)
	}
	c := s3.NewFromConfig(cfg)
	p.Client = c
	return c, nil
}

// parseCSV is the pure parsing path — extracted so tests can feed
// CSV strings directly without going through S3.
func parseCSV(r io.Reader, cap int) ([]discovery.DiscoveredBusiness, error) {
	cr := csv.NewReader(r)
	// Allow rows with variable column counts so extra trailing
	// columns added by the operator's spreadsheet tool don't
	// reject the whole file.
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err == io.EOF {
		return []discovery.DiscoveredBusiness{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("csv: reading header: %w", err)
	}
	idx := indexHeaders(header)
	if _, ok := idx["name"]; !ok {
		return nil, fmt.Errorf("csv: header is missing `name` column (got %v)", header)
	}

	out := []discovery.DiscoveredBusiness{}
	for {
		if cap > 0 && len(out) >= cap {
			break
		}
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv: reading row %d: %w", len(out)+2, err)
		}
		name := pick(row, idx, "name")
		if name == "" {
			continue // skip — name is the only required field
		}
		out = append(out, discovery.DiscoveredBusiness{
			Name:       name,
			Domain:     pick(row, idx, "domain"),
			Location:   pick(row, idx, "location"),
			Vertical:   pick(row, idx, "vertical"),
			Source:     providerID,
			SourceRefs: map[string]string{"csvRow": fmt.Sprintf("%d", len(out)+2)},
			Confidence: 1.0, // operator-supplied → fully trusted
		})
	}
	return out, nil
}

// indexHeaders builds a lowercase header-name → column-index map so
// pick() can extract by name regardless of column ordering or case.
func indexHeaders(header []string) map[string]int {
	out := make(map[string]int, len(header))
	for i, h := range header {
		out[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return out
}

func pick(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}
