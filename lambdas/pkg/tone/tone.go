// Package tone models the EmailToneProfile DynamoDB row + the CRUD
// operations the email-draft Lambda (iter 7.2), the email-tone-tuner
// (iter 9), and the future /settings/email-tone/<vertical> UI share.
// Schema mirrors .ralph/specs/02-data-model.md § "EmailToneProfile":
//
//	pk = "EMAIL_TONE#<vertical>"
//	sk = "PROFILE"
//
// One row per vertical; "default" is the fallback the email-draft
// Lambda reads when no vertical-specific profile exists. Operators
// tune vertical-specific profiles via the /settings/email-tone UI; the
// email-tone-tuner proposes deltas to subjectPatterns / openerPatterns
// / prohibitedPhrases.
//
// The email-draft Lambda reads a profile on every invocation and
// passes it into the `email.v1` Bedrock prompt. The `version` field is
// part of the email.v1 cache key — bump the version (via Update) to
// invalidate cached drafts for that vertical.
package tone

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

// ItemType is written to the `type` attribute (Scan filter for List).
const ItemType = "EmailToneProfile"

// DefaultVertical is the fallback key the email-draft Lambda reads when
// no vertical-specific profile exists. Operators MUST have a row at
// this key — Terraform seeds it on first apply.
const DefaultVertical = "default"

// Profile is the EmailToneProfile row.
type Profile struct {
	Vertical          string   `json:"vertical"          dynamodbav:"vertical"`
	SubjectPatterns   []string `json:"subjectPatterns"   dynamodbav:"subjectPatterns"`
	OpenerPatterns    []string `json:"openerPatterns"    dynamodbav:"openerPatterns"`
	ProhibitedPhrases []string `json:"prohibitedPhrases" dynamodbav:"prohibitedPhrases"`
	Signature         string   `json:"signature"         dynamodbav:"signature"`
	OptOutLine        string   `json:"optOutLine"        dynamodbav:"optOutLine"`
	Version           int      `json:"version"           dynamodbav:"version"`
	CreatedAt         string   `json:"createdAt"         dynamodbav:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"         dynamodbav:"updatedAt"`
	Etag              string   `json:"etag"              dynamodbav:"etag"`
}

// Validate checks the required fields the email.v1 prompt + its
// post-validator depend on. The optOutLine MUST be present (the
// post-validator rejects any draft missing it verbatim), and there
// must be at least one subject + opener pattern for the prompt to
// work from.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Vertical) == "" {
		return errors.New("vertical is required")
	}
	if strings.TrimSpace(p.OptOutLine) == "" {
		return errors.New("optOutLine is required")
	}
	if len(p.SubjectPatterns) < 1 {
		return errors.New("subjectPatterns must have at least one entry")
	}
	if len(p.OpenerPatterns) < 1 {
		return errors.New("openerPatterns must have at least one entry")
	}
	if p.Version < 1 {
		return errors.New("version must be >= 1")
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
		panic(fmt.Errorf("tone: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

// ErrInvalid signals a validation failure.
var ErrInvalid = errors.New("tone: invalid")

// ErrEtagMismatch is returned by Update when the supplied etag doesn't
// match the row currently in DDB.
var ErrEtagMismatch = errors.New("tone: etag mismatch")

// ErrNotFound is returned by Get when no row exists for the vertical.
var ErrNotFound = errors.New("tone: not found")

func pkFor(vertical string) string { return "EMAIL_TONE#" + strings.ToLower(vertical) }

// Get returns the profile for a vertical. Returns ErrNotFound when the
// row is absent so callers can errors.Is.
func Get(ctx context.Context, vertical string) (Profile, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return Profile{}, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pkFor(vertical)},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		return Profile{}, fmt.Errorf("tone: GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return Profile{}, ErrNotFound
	}
	var p Profile
	if err := attributevalue.UnmarshalMap(out.Item, &p); err != nil {
		return Profile{}, fmt.Errorf("tone: unmarshal: %w", err)
	}
	return p, nil
}

// GetOrDefault returns the vertical-specific profile if it exists,
// otherwise falls back to the "default" row. Convenience wrapper for
// the email-draft Lambda (iter 7.2).
func GetOrDefault(ctx context.Context, vertical string) (Profile, error) {
	if vertical != "" && !strings.EqualFold(vertical, DefaultVertical) {
		p, err := Get(ctx, vertical)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Profile{}, err
		}
	}
	return Get(ctx, DefaultVertical)
}

// List returns every profile. Used by the /settings/email-tone index
// UI; O(n) Scan, n stays small (one row per active vertical).
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
			":type": &dtypes.AttributeValueMemberS{Value: ItemType},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("tone: Scan: %w", err)
	}
	profiles := make([]Profile, 0, len(out.Items))
	for _, item := range out.Items {
		var p Profile
		if err := attributevalue.UnmarshalMap(item, &p); err != nil {
			return nil, fmt.Errorf("tone: unmarshal scan row: %w", err)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// Put creates or replaces a profile. Mostly for tests + the seed path;
// production updates go via Update so etag protection kicks in.
func Put(ctx context.Context, p Profile) (Profile, error) {
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	now := nowFunc().Format(time.RFC3339)
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	p.Etag = newEtagFunc(16)
	return p, putItem(ctx, p)
}

// Update overwrites the row identified by vertical with the new
// content (after validation + etag check). Bumps Version so the
// email-draft Lambda's cache key changes — see the package docstring.
func Update(ctx context.Context, vertical string, p Profile, expectedEtag string) (Profile, error) {
	if expectedEtag == "" {
		return Profile{}, fmt.Errorf("%w: expected etag is required", ErrInvalid)
	}
	current, err := Get(ctx, vertical)
	if err != nil {
		return Profile{}, err
	}
	if current.Etag != expectedEtag {
		return Profile{}, ErrEtagMismatch
	}

	p.Vertical = vertical
	if p.Version <= current.Version {
		// Auto-bump if the caller forgot. email.v1 caches by version,
		// so two writes must never share a version.
		p.Version = current.Version + 1
	}
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	p.CreatedAt = current.CreatedAt
	p.UpdatedAt = nowFunc().Format(time.RFC3339)
	p.Etag = newEtagFunc(16)
	return p, putItem(ctx, p)
}

func putItem(ctx context.Context, p Profile) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(p)
	if err != nil {
		return fmt.Errorf("tone: marshal: %w", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: pkFor(p.Vertical)}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: "PROFILE"}
	item["type"] = &dtypes.AttributeValueMemberS{Value: ItemType}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("tone: PutItem: %w", err)
	}
	return nil
}
