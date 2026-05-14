// Package style models the VerticalStyleGuide DynamoDB row + the CRUD
// operations the spec-generator (iter 4.2) and the future
// /settings/style/<vertical> UI share. Schema mirrors
// .ralph/specs/02-data-model.md § "Vertical Style Guide":
//
//	pk = "STYLE#<vertical>"
//	sk = "PROFILE"
//
// VerticalStyleGuide is keyed by vertical (lowercased, hyphenated).
// One row per vertical; "default" is the fallback the spec generator
// reads when no vertical-specific guide exists. Operators tune
// vertical-specific guides via the /settings/style UI.
//
// The spec-generator reads a guide on every invocation and passes the
// content into the `spec.v1` Bedrock prompt's `<style_guide>` block.
// The `version` field becomes part of the spec.v1 cache key — bump
// the version (via Update) to invalidate cached specs for that
// vertical.
package style

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

// DefaultVertical is the fallback key the spec-generator reads when
// no vertical-specific guide exists. Operators MUST have a row at
// this key — Terraform seeds it on first apply.
const DefaultVertical = "default"

// Guide is the VerticalStyleGuide row.
type Guide struct {
	Vertical     string   `json:"vertical"     dynamodbav:"vertical"`
	Tone         string   `json:"tone"         dynamodbav:"tone"`
	DoPhrases    []string `json:"doPhrases"    dynamodbav:"doPhrases"`
	DontPhrases  []string `json:"dontPhrases"  dynamodbav:"dontPhrases"`
	AntiPatterns []string `json:"antiPatterns" dynamodbav:"antiPatterns"`
	Palette      Palette  `json:"palette"      dynamodbav:"palette"`
	Version      int      `json:"version"      dynamodbav:"version"`
	CreatedAt    string   `json:"createdAt"    dynamodbav:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"    dynamodbav:"updatedAt"`
	Etag         string   `json:"etag"         dynamodbav:"etag"`
}

// Palette is the per-vertical colour scheme. Primary is the main brand
// colour; neutral is a 3-shade dark→mid→light ramp.
type Palette struct {
	Primary string   `json:"primary" dynamodbav:"primary"`
	Neutral []string `json:"neutral" dynamodbav:"neutral"`
}

// Validate checks required scalar fields are present and the palette
// has at least one neutral shade. Spec-generator-side checks (e.g.
// "primary contrasts with neutral[2]") live on the AI prompt's
// post-validator, not here.
func (g Guide) Validate() error {
	if strings.TrimSpace(g.Vertical) == "" {
		return errors.New("vertical is required")
	}
	if strings.TrimSpace(g.Tone) == "" {
		return errors.New("tone is required")
	}
	if g.Version < 1 {
		return errors.New("version must be >= 1")
	}
	if strings.TrimSpace(g.Palette.Primary) == "" {
		return errors.New("palette.primary is required")
	}
	if len(g.Palette.Neutral) < 1 {
		return errors.New("palette.neutral must have at least one shade")
	}
	return nil
}

// nowFunc + etag generator overrides for deterministic tests.
var (
	nowFunc     = func() time.Time { return time.Now().UTC() }
	newEtagFunc = randomHex
)

// SetNowFunc overrides the time source. Tests only.
func SetNowFunc(f func() time.Time) { nowFunc = f }

// SetEtagFunc overrides the etag generator. Tests only.
func SetEtagFunc(f func(int) string) { newEtagFunc = f }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("style: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

// ErrInvalid signals a validation failure.
var ErrInvalid = errors.New("style: invalid")

// ErrEtagMismatch is returned by Update when the supplied etag doesn't
// match the row currently in DDB.
var ErrEtagMismatch = errors.New("style: etag mismatch")

// ErrNotFound is returned by Get when no row exists for the vertical.
var ErrNotFound = errors.New("style: not found")

func pkFor(vertical string) string { return "STYLE#" + strings.ToLower(vertical) }

// Get returns the guide for a vertical. If the vertical-specific row
// doesn't exist, callers can fall back to Get(ctx, DefaultVertical).
// Returns ErrNotFound when the row is absent (rather than a nil
// pointer dance) so callers can errors.Is.
func Get(ctx context.Context, vertical string) (Guide, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return Guide{}, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pkFor(vertical)},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		return Guide{}, fmt.Errorf("style: GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return Guide{}, ErrNotFound
	}
	var g Guide
	if err := attributevalue.UnmarshalMap(out.Item, &g); err != nil {
		return Guide{}, fmt.Errorf("style: unmarshal: %w", err)
	}
	return g, nil
}

// GetOrDefault returns the vertical-specific guide if it exists,
// otherwise falls back to the "default" row. Convenience wrapper for
// the spec-generator (iter 4.2).
func GetOrDefault(ctx context.Context, vertical string) (Guide, error) {
	if vertical != "" && !strings.EqualFold(vertical, DefaultVertical) {
		g, err := Get(ctx, vertical)
		if err == nil {
			return g, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Guide{}, err
		}
	}
	return Get(ctx, DefaultVertical)
}

// List returns every guide. Used by the /settings/style index UI;
// O(n) scan, n stays small (one row per active vertical, <100 across
// the project's life).
func List(ctx context.Context) ([]Guide, error) {
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
			":type": &dtypes.AttributeValueMemberS{Value: "VerticalStyleGuide"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("style: Scan: %w", err)
	}
	guides := make([]Guide, 0, len(out.Items))
	for _, item := range out.Items {
		var g Guide
		if err := attributevalue.UnmarshalMap(item, &g); err != nil {
			return nil, fmt.Errorf("style: unmarshal scan row: %w", err)
		}
		guides = append(guides, g)
	}
	return guides, nil
}

// Put creates or replaces a guide. Mostly for tests + the seed
// path; production updates go via Update so etag protection kicks
// in.
func Put(ctx context.Context, g Guide) (Guide, error) {
	if err := g.Validate(); err != nil {
		return Guide{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	now := nowFunc().Format(time.RFC3339)
	if g.CreatedAt == "" {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	g.Etag = newEtagFunc(16)
	return g, putItem(ctx, g)
}

// Update overwrites the row identified by vertical with the new
// content (after validation + etag check). Bumps Version so the
// spec-generator's cache key changes — see the package docstring.
func Update(ctx context.Context, vertical string, g Guide, expectedEtag string) (Guide, error) {
	if expectedEtag == "" {
		return Guide{}, fmt.Errorf("%w: expected etag is required", ErrInvalid)
	}
	current, err := Get(ctx, vertical)
	if err != nil {
		return Guide{}, err
	}
	if current.Etag != expectedEtag {
		return Guide{}, ErrEtagMismatch
	}

	g.Vertical = vertical
	if g.Version <= current.Version {
		// Auto-bump if the caller forgot. Style-guide consumers cache
		// by version, so two writes must never share a version.
		g.Version = current.Version + 1
	}
	if err := g.Validate(); err != nil {
		return Guide{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	g.CreatedAt = current.CreatedAt
	g.UpdatedAt = nowFunc().Format(time.RFC3339)
	g.Etag = newEtagFunc(16)
	return g, putItem(ctx, g)
}

func putItem(ctx context.Context, g Guide) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(g)
	if err != nil {
		return fmt.Errorf("style: marshal: %w", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: pkFor(g.Vertical)}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: "PROFILE"}
	item["type"] = &dtypes.AttributeValueMemberS{Value: "VerticalStyleGuide"}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("style: PutItem: %w", err)
	}
	return nil
}
