// Package main is the passcode-cleanup Lambda (iter 8.5). An hourly
// EventBridge Scheduler invokes it; it wipes the KMS-encrypted
// passcode cleartext (Website.passcodeCipher) ~24h after the outreach
// email was sent.
//
// The sender (iter 8.2) stamps Website.passcodeCleanupDueAt = sentAt +
// 24h. This sweep finds Websites whose due time has passed and that
// still carry a passcodeCipher, REMOVEs the cipher (+ the due marker),
// and publishes preview.passcode.cleartext_wiped.
//
// Only passcodeCipher is touched — passcodeHash and the Cloudflare KV
// mapping are left intact, so the recipient's preview link keeps
// working; only the operator's ability to re-view the cleartext goes
// away (they regenerate to resend).
//
// Privacy: the cleartext is NEVER read, logged, or emitted. The sweep
// projects only pk/sk, REMOVEs the attribute blind, and the event
// carries ids only. NOT kill-switch gated — this is a privacy/
// retention guarantee that must run regardless of outreach state.
// iter 8.6 extends the same sweep with the passcodeRevealableUntil
// backstop.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

const consumerName = "passcode-cleanup"

type websiteRef struct {
	BusinessID string
	WebsiteID  string
}

// WipedDetail is the preview.passcode.cleartext_wiped payload — ids
// only, never the cleartext.
type WipedDetail struct {
	BusinessID string `json:"businessId"`
	WebsiteID  string `json:"websiteId"`
	Reason     string `json:"reason"`
	WipedAt    string `json:"wipedAt"`
}

type runDeps struct {
	// ScanDue returns Websites whose passcodeCleanupDueAt has passed
	// and that still have a passcodeCipher.
	ScanDue func(ctx context.Context, nowUnix int64) ([]websiteRef, error)
	// Wipe REMOVEs passcodeCipher (+ the due marker). Returns false if
	// the cipher was already gone (concurrent run / nothing to do).
	Wipe    func(ctx context.Context, ref websiteRef, nowRFC string) (bool, error)
	Publish func(ctx context.Context, d WipedDetail) error
	Now     func() time.Time
}

func handle(ctx context.Context) error {
	logger := applog.FromContext(ctx)
	deps, err := buildDeps(ctx)
	if err != nil {
		return err
	}
	return sweep(ctx, deps, logger)
}

func sweep(ctx context.Context, d runDeps, logger *slog.Logger) error {
	now := d.Now().UTC()
	refs, err := d.ScanDue(ctx, now.Unix())
	if err != nil {
		return fmt.Errorf("scan due websites: %w", err)
	}
	nowRFC := now.Format(time.RFC3339)
	wiped, failed := 0, 0
	for _, ref := range refs {
		did, werr := d.Wipe(ctx, ref, nowRFC)
		if werr != nil {
			// One bad item must not abort the sweep — the next run
			// retries it (the due marker is still set).
			logger.Error("passcode_cleanup.wipe_failed", "businessId", ref.BusinessID, "websiteId", ref.WebsiteID, "err", werr.Error())
			failed++
			continue
		}
		if !did {
			continue // already wiped by a concurrent run
		}
		if perr := d.Publish(ctx, WipedDetail{
			BusinessID: ref.BusinessID,
			WebsiteID:  ref.WebsiteID,
			Reason:     "sent_24h",
			WipedAt:    nowRFC,
		}); perr != nil {
			logger.Error("passcode_cleanup.publish_failed", "businessId", ref.BusinessID, "websiteId", ref.WebsiteID, "err", perr.Error())
			failed++
			continue
		}
		wiped++
	}
	logger.Info("passcode_cleanup.sweep_done", "candidates", len(refs), "wiped", wiped, "failed", failed)
	if failed > 0 {
		return fmt.Errorf("passcode-cleanup: %d/%d wipes failed", failed, len(refs))
	}
	return nil
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	if _, err := awsconfig.LoadDefaultConfig(ctx); err != nil {
		return runDeps{}, fmt.Errorf("passcode-cleanup: AWS config: %w", err)
	}
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		ScanDue: scanDue,
		Wipe:    wipe,
		Publish: func(ctx context.Context, det WipedDetail) error {
			return pkgevents.Publish(ctx, publisher, pkgevents.New("preview.passcode.cleartext_wiped", consumerName, det))
		},
		Now: time.Now,
	}, nil
}

func scanDue(ctx context.Context, nowUnix int64) ([]websiteRef, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	var (
		refs    []websiteRef
		lastKey map[string]dtypes.AttributeValue
	)
	for {
		out, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName: aws.String(ddb.TableName()),
			// Project ONLY the keys — never pull passcodeCipher into
			// process memory.
			ProjectionExpression:     aws.String("pk, sk"),
			FilterExpression:         aws.String("#t = :w AND attribute_exists(passcodeCipher) AND passcodeCleanupDueAt <= :now"),
			ExpressionAttributeNames: map[string]string{"#t": "type"},
			ExpressionAttributeValues: map[string]dtypes.AttributeValue{
				":w":   &dtypes.AttributeValueMemberS{Value: "Website"},
				":now": &dtypes.AttributeValueMemberN{Value: strconv.FormatInt(nowUnix, 10)},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		for _, it := range out.Items {
			pk, _ := it["pk"].(*dtypes.AttributeValueMemberS)
			sk, _ := it["sk"].(*dtypes.AttributeValueMemberS)
			if pk == nil || sk == nil {
				continue
			}
			refs = append(refs, websiteRef{
				BusinessID: strings.TrimPrefix(pk.Value, "BUSINESS#"),
				WebsiteID:  strings.TrimPrefix(sk.Value, "WEBSITE#"),
			})
		}
		if len(out.LastEvaluatedKey) == 0 {
			return refs, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

func wipe(ctx context.Context, ref websiteRef, nowRFC string) (bool, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return false, err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + ref.BusinessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#" + ref.WebsiteID},
		},
		UpdateExpression:    aws.String("REMOVE passcodeCipher, passcodeCleanupDueAt SET updatedAt = :ts"),
		ConditionExpression: aws.String("attribute_exists(passcodeCipher)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":ts": &dtypes.AttributeValueMemberS{Value: nowRFC},
		},
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return false, nil // already wiped — not an error
		}
		return false, fmt.Errorf("wipe website %s: %w", ref.WebsiteID, err)
	}
	return true, nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
