// Package main is the publisher Lambda. EventBridge routes
// `website.generated` events here via an SQS main queue. Per record:
//
//  1. Decode the envelope, idempotency-wrap on env.EventID.
//  2. Load Business + Website rows; defensive guard
//     `Website.Status == "generated"`.
//  3. Read the generated HTML from S3 (the s3Key carried on the event).
//  4. Upload it to Cloudflare R2 at `sites/<websiteId>/index.html` via
//     the R2 S3-compatible API. R2 credentials come from SSM
//     (separate from the Lambda's AWS creds).
//  5. Generate an 8-char Crockford-Base32 passcode, hash it, KMS-
//     encrypt the cleartext, write the hash to Workers KV via the
//     Cloudflare REST API. **Cleartext is never logged.**
//  6. Update the Website row: passcodeHash, passcodeCipher,
//     passcodeRevealableUntil = now + 7d, previewUrl, status="published".
//  7. Publish `website.published` (NO cleartext).
//
// Entry-level killswitch wraps StagePreview (publisher rolls up to
// preview per killswitch.StageMap).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/passcode"
)

const consumerName = "publisher"

// PasscodeRevealableWindow is how long the cleartext stays accessible
// via `passcodeCipher` before the cleanup Lambda (iter 8.5) wipes it.
const PasscodeRevealableWindow = 7 * 24 * time.Hour

// WebsiteGeneratedDetail mirrors lambdas/generator's emitted payload.
type WebsiteGeneratedDetail struct {
	BusinessID string `json:"businessId"`
	SpecID     string `json:"specId"`
	WebsiteID  string `json:"websiteId"`
	S3Key      string `json:"s3Key"`
}

// WebsitePublishedDetail is the published event payload per 03-events.md.
// Never carries the passcode cleartext.
type WebsitePublishedDetail struct {
	BusinessID              string `json:"businessId"`
	WebsiteID               string `json:"websiteId"`
	PreviewURL              string `json:"previewUrl"`
	LighthouseScore         int    `json:"lighthouseScore,omitempty"`
	PasscodeIssued          bool   `json:"passcodeIssued"`
	PasscodeRevealableUntil string `json:"passcodeRevealableUntil"`
}

// S3API is the subset of *s3.Client we need (both AWS S3 and R2 use it).
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// KMSAPI is the subset we need; matches passcode.KMSAPI.
type KMSAPI = passcode.KMSAPI

// KVPutter is the subset of pkg/passcode.KVWriter we exercise so tests
// can inject a fake without making real Cloudflare API calls.
type KVPutter interface {
	Put(ctx context.Context, key, value string, metadata map[string]string) error
}

// runDeps is the testable surface.
type runDeps struct {
	GetBusiness  func(ctx context.Context, businessID string) (*BusinessRow, error)
	GetWebsite   func(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error)
	GetHTML      func(ctx context.Context, key string) ([]byte, error)
	PutR2        func(ctx context.Context, key string, body []byte) error
	GeneratePass func() (string, error)
	HashPass     func(passcode, salt string) string
	EncryptPass  func(ctx context.Context, cleartext string) (string, error)
	PutKV        func(ctx context.Context, key, value string, metadata map[string]string) error
	UpdateRow    func(ctx context.Context, row WebsiteRow) error
	Publish      func(ctx context.Context, env pkgevents.Envelope[WebsitePublishedDetail]) error
	Now          func() time.Time

	// Config — injected at buildDeps time.
	PasscodeSalt   string
	PreviewURLBase string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StagePreview, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[WebsiteGeneratedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[WebsiteGeneratedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteGeneratedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"websiteId", env.Detail.WebsiteID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("publisher.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteGeneratedDetail], logger *slog.Logger) error {
	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("publisher: get business: %w", err)
	}
	if biz == nil {
		logger.Warn("publisher.business.missing")
		return nil
	}
	website, err := d.GetWebsite(ctx, env.Detail.BusinessID, env.Detail.WebsiteID)
	if err != nil {
		return fmt.Errorf("publisher: get website: %w", err)
	}
	if website == nil {
		logger.Warn("publisher.website.missing")
		return nil
	}
	// Defensive: only publish a freshly-generated bundle. A replay
	// after publish-success would otherwise overwrite the live row.
	if website.Status != "generated" {
		logger.Warn("publisher.website.not_generated", "status", website.Status)
		return nil
	}

	html, err := d.GetHTML(ctx, env.Detail.S3Key)
	if err != nil {
		return fmt.Errorf("publisher: read html from s3: %w", err)
	}
	r2Key := fmt.Sprintf("sites/%s/index.html", website.ID)
	if err := d.PutR2(ctx, r2Key, html); err != nil {
		return fmt.Errorf("publisher: put r2: %w", err)
	}

	// --- passcode issuance ----
	// NEVER LOG THE CLEARTEXT. The cleartext lives in `code` only;
	// it's hashed + KMS-encrypted before any logger reaches it, and
	// it's never put into a struct field or a returned value.
	// (Go strings are immutable so we can't zero the backing array
	// before GC — defense-in-depth zeroing would require a []byte
	// pipeline through pkg/passcode, deferred until a tuner shows
	// a real cleartext-leak surface.)
	code, err := d.GeneratePass()
	if err != nil {
		return fmt.Errorf("publisher: generate passcode: %w", err)
	}
	hash := d.HashPass(code, d.PasscodeSalt)
	cipher, err := d.EncryptPass(ctx, code)
	if err != nil {
		return fmt.Errorf("publisher: kms encrypt: %w", err)
	}
	if err := d.PutKV(ctx, "passcode:"+website.ID, hash, map[string]string{
		"businessId": biz.ID, "websiteId": website.ID,
	}); err != nil {
		return fmt.Errorf("publisher: kv put: %w", err)
	}

	now := d.Now().UTC()
	revealableUntil := now.Add(PasscodeRevealableWindow)
	updated := *website
	updated.PasscodeHash = hash
	updated.PasscodeCipher = cipher
	updated.PasscodeRevealableUntil = revealableUntil.Unix()
	updated.PreviewURL = fmt.Sprintf("%s/sites/%s", d.PreviewURLBase, website.ID)
	updated.Status = "published"
	updated.UpdatedAt = now.Format(time.RFC3339)
	if err := d.UpdateRow(ctx, updated); err != nil {
		return fmt.Errorf("publisher: update website row: %w", err)
	}

	out := pkgevents.New("website.published", consumerName, WebsitePublishedDetail{
		BusinessID:              biz.ID,
		WebsiteID:               website.ID,
		PreviewURL:              updated.PreviewURL,
		PasscodeIssued:          true,
		PasscodeRevealableUntil: revealableUntil.Format(time.RFC3339),
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		return fmt.Errorf("publisher: publish website.published: %w", err)
	}
	logger.Info("publisher.completed",
		"websiteId", website.ID,
		"previewUrl", updated.PreviewURL,
		"r2Key", r2Key,
		// Deliberately omit hash + cipher + the cleartext (which is gone).
	)
	return nil
}

// --- row shapes ---------------------------------------------------------

type BusinessRow struct {
	ID       string `dynamodbav:"id"`
	Name     string `dynamodbav:"name"`
	Domain   string `dynamodbav:"domain"`
	Vertical string `dynamodbav:"vertical"`
}

// WebsiteRow mirrors 02-data-model.md § "Website". Passcode fields
// are filled by this Lambda on publish.
type WebsiteRow struct {
	PK                      string `dynamodbav:"pk"`
	SK                      string `dynamodbav:"sk"`
	Type                    string `dynamodbav:"type"`
	ID                      string `dynamodbav:"id"`
	SpecID                  string `dynamodbav:"specId"`
	R2Prefix                string `dynamodbav:"r2Prefix"`
	Status                  string `dynamodbav:"status"`
	PreviewURL              string `dynamodbav:"previewUrl,omitempty"`
	PasscodeHash            string `dynamodbav:"passcodeHash,omitempty"`
	PasscodeCipher          string `dynamodbav:"passcodeCipher,omitempty"`
	PasscodeRevealableUntil int64  `dynamodbav:"passcodeRevealableUntil,omitempty"`
	CreatedAt               string `dynamodbav:"createdAt"`
	UpdatedAt               string `dynamodbav:"updatedAt"`
	Etag                    string `dynamodbav:"etag"`
}

// --- AWS / Cloudflare wiring (production) ----------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("publisher: AWS config: %w", err)
	}
	blobsBucket := os.Getenv("BLOBS_BUCKET")
	if blobsBucket == "" {
		return runDeps{}, errors.New("publisher: BLOBS_BUCKET is not set")
	}
	r2AccountID := os.Getenv("R2_ACCOUNT_ID")
	r2Bucket := os.Getenv("R2_BUCKET")
	r2AccessKey := os.Getenv("R2_ACCESS_KEY_ID")
	r2Secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	if r2AccountID == "" || r2Bucket == "" || r2AccessKey == "" || r2Secret == "" {
		return runDeps{}, errors.New("publisher: R2_* env vars are not set")
	}
	cfAccountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	cfKVNamespace := os.Getenv("CLOUDFLARE_KV_NAMESPACE_ID")
	cfAPIToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	if cfAccountID == "" || cfKVNamespace == "" || cfAPIToken == "" {
		return runDeps{}, errors.New("publisher: CLOUDFLARE_* env vars are not set")
	}
	salt := os.Getenv("PASSCODE_SALT")
	if salt == "" {
		return runDeps{}, errors.New("publisher: PASSCODE_SALT is not set")
	}
	kmsKeyID := os.Getenv("PASSCODE_KMS_KEY_ID")
	if kmsKeyID == "" {
		return runDeps{}, errors.New("publisher: PASSCODE_KMS_KEY_ID is not set")
	}
	previewURLBase := os.Getenv("PREVIEW_URL_BASE")
	if previewURLBase == "" {
		return runDeps{}, errors.New("publisher: PREVIEW_URL_BASE is not set")
	}

	awsS3 := s3.NewFromConfig(cfg)
	r2 := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Region = "auto"
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", r2AccountID))
		o.Credentials = credentials.NewStaticCredentialsProvider(r2AccessKey, r2Secret, "")
		o.UsePathStyle = false
	})
	kmsClient := kms.NewFromConfig(cfg)
	kv := passcode.NewKVWriter(cfAccountID, cfKVNamespace, cfAPIToken)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}

	return runDeps{
		GetBusiness:  getBusiness,
		GetWebsite:   getWebsite,
		GetHTML:      getHTMLFn(awsS3, blobsBucket),
		PutR2:        putR2Fn(r2, r2Bucket),
		GeneratePass: passcode.Generate,
		HashPass:     passcode.Hash,
		EncryptPass: func(ctx context.Context, cleartext string) (string, error) {
			return passcode.EncryptCleartext(ctx, kmsClient, kmsKeyID, cleartext)
		},
		PutKV:     kv.Put,
		UpdateRow: putWebsite,
		Publish: func(ctx context.Context, env pkgevents.Envelope[WebsitePublishedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		Now:            time.Now,
		PasscodeSalt:   salt,
		PreviewURLBase: previewURLBase,
	}, nil
}

func getBusiness(ctx context.Context, businessID string) (*BusinessRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get business %s: %w", businessID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var b BusinessRow
	if err := attributevalue.UnmarshalMap(out.Item, &b); err != nil {
		return nil, fmt.Errorf("unmarshal business %s: %w", businessID, err)
	}
	return &b, nil
}

func getWebsite(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#" + websiteID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get website %s/%s: %w", businessID, websiteID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var w WebsiteRow
	if err := attributevalue.UnmarshalMap(out.Item, &w); err != nil {
		return nil, fmt.Errorf("unmarshal website %s: %w", websiteID, err)
	}
	return &w, nil
}

func getHTMLFn(client S3API, bucket string) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, key string) ([]byte, error) {
		out, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key),
		})
		if err != nil {
			return nil, fmt.Errorf("s3 GetObject %s: %w", key, err)
		}
		defer func() { _ = out.Body.Close() }()
		return io.ReadAll(out.Body)
	}
}

func putR2Fn(client S3API, bucket string) func(context.Context, string, []byte) error {
	return func(ctx context.Context, key string, body []byte) error {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String(bucket),
			Key:          aws.String(key),
			Body:         bytes.NewReader(body),
			ContentType:  aws.String("text/html; charset=utf-8"),
			CacheControl: aws.String("public, max-age=300"),
		})
		if err != nil {
			return fmt.Errorf("r2 PutObject %s: %w", key, err)
		}
		return nil
	}
}

func putWebsite(ctx context.Context, row WebsiteRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal website: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put website %s: %w", row.ID, err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
