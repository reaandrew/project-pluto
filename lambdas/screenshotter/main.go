// Package main is the screenshotter Lambda (iter 5.5b). EventBridge
// routes `website.published` events here via an SQS main queue. Per
// record:
//
//  1. Decode the envelope, idempotency-wrap on env.EventID.
//  2. Load the Website row; defensive guard `Status == "published"`.
//  3. Mint a short-lived operator-bypass token and append it as
//     `?op=<token>` to the preview URL so the Cloudflare Browser
//     Rendering headless browser can render past the passcode gate.
//  4. Capture a desktop + a mobile screenshot via the Browser Rendering
//     REST API (one cost.WithCostCap around the pair).
//  5. Upload both PNGs to R2 at `screenshots/<websiteId>/<size>.png`.
//  6. UpdateItem the Website row's `screenshots` map (no full-row
//     read-modify-write, so no other field can be clobbered).
//
// No outbound event: 03-events.md defines none for this step and the
// queue (iter 6) reads the Website row directly. Entry-level killswitch
// wraps StagePreview.
//
// SECURITY: the `?op=` token is a bearer capability. It is never logged
// and never persisted — only placed in the Browser Rendering request
// body over HTTPS. Error paths log the HTTP status only, never the
// target URL or request body.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/passcode"
)

const consumerName = "screenshotter"

// PerScreenshotUSD is the Cloudflare Browser Rendering unit price per
// .ralph/specs/05-capacity-and-cost.md. Two shots per website.
const PerScreenshotUSD = 0.0005

// shot is one (label, viewport) the screenshotter captures. The labels
// are the canonical sizes in 02-data-model.md § Website.screenshots.
type shot struct {
	Label  string
	Width  int
	Height int
}

var shots = []shot{
	{Label: "desktop", Width: 1280, Height: 800},
	{Label: "mobile", Width: 390, Height: 844},
}

// WebsitePublishedDetail mirrors the publisher's emitted payload
// (03-events.md § website.published). Never carries cleartext.
type WebsitePublishedDetail struct {
	BusinessID string `json:"businessId"`
	WebsiteID  string `json:"websiteId"`
	PreviewURL string `json:"previewUrl"`
}

// WebsiteRow is the slice of the Website item this Lambda reads for its
// status guard. The write is a targeted UpdateItem, not a marshal of
// this struct, so omitted fields are never clobbered.
type WebsiteRow struct {
	ID     string `dynamodbav:"id"`
	Status string `dynamodbav:"status"`
}

// runDeps is the testable surface.
type runDeps struct {
	GetWebsite     func(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error)
	Screenshot     func(ctx context.Context, targetURL string, width, height int) ([]byte, error)
	PutR2          func(ctx context.Context, key string, body []byte) error
	SetScreenshots func(ctx context.Context, businessID, websiteID string, urls map[string]string, now time.Time) error
	Cap            func(ctx context.Context) (float64, error)
	Now            func() time.Time

	PasscodeSalt string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StagePreview, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[WebsitePublishedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[WebsitePublishedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsitePublishedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"websiteId", env.Detail.WebsiteID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("screenshotter.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsitePublishedDetail], logger *slog.Logger) error {
	businessID := env.Detail.BusinessID
	websiteID := env.Detail.WebsiteID

	web, err := d.GetWebsite(ctx, businessID, websiteID)
	if err != nil {
		return fmt.Errorf("screenshotter: get website: %w", err)
	}
	if web == nil {
		logger.Warn("screenshotter.website.missing")
		return nil
	}
	// Only screenshot a live published preview. A replay after the
	// operator rejected/regenerated must not re-shoot a stale state.
	if web.Status != "published" {
		logger.Warn("screenshotter.website.not_published", "status", web.Status)
		return nil
	}

	previewURL := env.Detail.PreviewURL
	if previewURL == "" {
		return errors.New("screenshotter: website.published carried no previewUrl")
	}
	base := strings.TrimSuffix(previewURL, "/sites/"+websiteID)

	now := d.Now().UTC()
	// Token is a bearer capability — never logged, never persisted.
	token := passcode.SignOpToken(websiteID, d.PasscodeSalt, now)
	targetURL := previewURL + "?op=" + token

	capUSD, err := d.Cap(ctx)
	if err != nil {
		return fmt.Errorf("screenshotter: read budget cap: %w", err)
	}
	estimate := float64(len(shots)) * PerScreenshotUSD

	urls, err := cost.WithCostCap(ctx, killswitch.StagePreview, estimate, capUSD,
		func(ctx context.Context) (map[string]string, float64, error) {
			out := make(map[string]string, len(shots))
			for _, s := range shots {
				png, err := d.Screenshot(ctx, targetURL, s.Width, s.Height)
				if err != nil {
					return nil, 0, fmt.Errorf("screenshotter: render %s: %w", s.Label, err)
				}
				key := fmt.Sprintf("screenshots/%s/%s.png", websiteID, s.Label)
				if err := d.PutR2(ctx, key, png); err != nil {
					return nil, 0, fmt.Errorf("screenshotter: put r2 %s: %w", s.Label, err)
				}
				out[s.Label] = fmt.Sprintf("%s/screenshots/%s/%s.png", base, websiteID, s.Label)
			}
			return out, float64(len(shots)) * PerScreenshotUSD, nil
		})
	if err != nil {
		return err
	}

	if err := d.SetScreenshots(ctx, businessID, websiteID, urls, now); err != nil {
		return fmt.Errorf("screenshotter: update website screenshots: %w", err)
	}

	logger.Info("screenshotter.completed",
		"websiteId", websiteID,
		"sizes", len(urls),
		// Deliberately omit targetURL (carries the op token).
	)
	return nil
}

// --- AWS / Cloudflare wiring (production) ----------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("screenshotter: AWS config: %w", err)
	}
	r2AccountID := os.Getenv("R2_ACCOUNT_ID")
	r2Bucket := os.Getenv("R2_BUCKET")
	r2AccessKey := os.Getenv("R2_ACCESS_KEY_ID")
	r2Secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	if r2AccountID == "" || r2Bucket == "" || r2AccessKey == "" || r2Secret == "" {
		return runDeps{}, errors.New("screenshotter: R2_* env vars are not set")
	}
	cfAccountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	brToken := os.Getenv("CLOUDFLARE_BROWSER_RENDERING_TOKEN")
	if cfAccountID == "" || brToken == "" {
		return runDeps{}, errors.New("screenshotter: CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_BROWSER_RENDERING_TOKEN are not set")
	}
	salt := os.Getenv("PASSCODE_SALT")
	if salt == "" {
		return runDeps{}, errors.New("screenshotter: PASSCODE_SALT is not set")
	}

	r2 := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Region = "auto"
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", r2AccountID))
		o.Credentials = credentials.NewStaticCredentialsProvider(r2AccessKey, r2Secret, "")
		o.UsePathStyle = false
	})

	return runDeps{
		GetWebsite:     getWebsite,
		Screenshot:     browserRenderingFn(cfAccountID, brToken),
		PutR2:          putR2Fn(r2, r2Bucket),
		SetScreenshots: setScreenshots,
		Cap: func(ctx context.Context) (float64, error) {
			return killswitch.CapUSD(ctx, killswitch.StagePreview)
		},
		Now:          time.Now,
		PasscodeSalt: salt,
	}, nil
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

// setScreenshots writes only the screenshots map + updatedAt via a
// targeted UpdateItem so no other Website field is touched.
func setScreenshots(ctx context.Context, businessID, websiteID string, urls map[string]string, now time.Time) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	av, err := attributevalue.Marshal(urls)
	if err != nil {
		return fmt.Errorf("marshal screenshots: %w", err)
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#" + websiteID},
		},
		UpdateExpression: aws.String("SET screenshots = :s, updatedAt = :now"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":   av,
			":now": &dtypes.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
		},
	})
	if err != nil {
		return fmt.Errorf("update website %s screenshots: %w", websiteID, err)
	}
	return nil
}

func putR2Fn(client interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}, bucket string) func(context.Context, string, []byte) error {
	return func(ctx context.Context, key string, body []byte) error {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String(bucket),
			Key:          aws.String(key),
			Body:         bytes.NewReader(body),
			ContentType:  aws.String("image/png"),
			CacheControl: aws.String("private, max-age=300"),
		})
		if err != nil {
			return fmt.Errorf("r2 PutObject %s: %w", key, err)
		}
		return nil
	}
}

// browserRenderingFn calls the Cloudflare Browser Rendering REST API.
// Success returns the raw PNG bytes (content-type image/png). On a
// non-200 the status is logged but never the target URL/body (the URL
// carries the op token).
func browserRenderingFn(accountID, token string) func(context.Context, string, int, int) ([]byte, error) {
	endpoint := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/browser-rendering/screenshot", accountID)
	httpClient := &http.Client{Timeout: 45 * time.Second}
	return func(ctx context.Context, targetURL string, width, height int) ([]byte, error) {
		body, err := json.Marshal(map[string]any{
			"url": targetURL,
			"viewport": map[string]int{
				"width":  width,
				"height": height,
			},
			"screenshotOptions": map[string]any{
				"type":     "png",
				"fullPage": true,
			},
			"gotoOptions": map[string]any{
				"waitUntil": "networkidle0",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("browser-rendering request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		png, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, fmt.Errorf("read browser-rendering response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			// Status only — the request URL carries the op token.
			return nil, fmt.Errorf("browser-rendering returned HTTP %d", resp.StatusCode)
		}
		return png, nil
	}
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
