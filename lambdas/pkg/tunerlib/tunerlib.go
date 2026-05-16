// Package tunerlib is the shared core of the three weekly tuner
// Lambdas (iter 9.3): tuner-targeting, tuner-style, tuner-email-tone.
// Each is a thin main that supplies its prompt + kind + feedback
// filter; tunerlib does the per-vertical loop: aggregate the last N
// days of operator Feedback, ask Bedrock for a conservative delta
// (via the injected Propose), and persist it as a PENDING TunerDelta +
// publish tuner.delta.proposed. Nothing is auto-applied (that is 9.4).
//
// Tuners are NOT kill-switch gated (killswitch.StageMap maps them to
// "") — they run on their own weekly schedule. The paid Bedrock call
// is still cost-capped from the Bedrock budget (StageAudit →
// DailyBedrockUsd, the reply-triage precedent); a cap hit skips the
// week (returns nil — a weekly job must not DLQ-loop).
package tunerlib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
)

// FeedbackRow is the slice of a pkg/feedback row a tuner reads. The
// originalPayload/editedPayload diffs are the signal; no passcode
// concern (the email path already redacted them to {{PASSCODE}} at
// capture time).
type FeedbackRow struct {
	Subject         string `dynamodbav:"subject" json:"subject"`
	Action          string `dynamodbav:"action" json:"action"`
	OriginalPayload string `dynamodbav:"originalPayload" json:"originalPayload,omitempty"`
	EditedPayload   string `dynamodbav:"editedPayload" json:"editedPayload,omitempty"`
	Notes           string `dynamodbav:"notes" json:"notes,omitempty"`
	CreatedAt       string `dynamodbav:"createdAt" json:"createdAt"`
}

// TunerDelta is the single PENDING proposal item all three tuners
// write (kind-discriminated). gsi1pk = "DELTA#STATUS#<status>" so the
// iter-9.4 /tuners UI lists every pending delta across kinds with one
// query.
type TunerDelta struct {
	PK              string `dynamodbav:"pk"`
	SK              string `dynamodbav:"sk"`
	Type            string `dynamodbav:"type"`
	ID              string `dynamodbav:"id"`
	Kind            string `dynamodbav:"kind"` // targeting | style | email_tone
	Vertical        string `dynamodbav:"vertical"`
	Status          string `dynamodbav:"status"` // pending | applied | dismissed
	ProposedPayload string `dynamodbav:"proposedPayload"`
	Rationale       string `dynamodbav:"rationale"`
	PromptID        string `dynamodbav:"promptId"`
	CreatedAt       string `dynamodbav:"createdAt"`
	UpdatedAt       string `dynamodbav:"updatedAt"`
	GSI1PK          string `dynamodbav:"gsi1pk"`
	GSI1SK          string `dynamodbav:"gsi1sk"`
}

// DeltaDetail is the tuner.delta.proposed payload — ids + rationale
// only; the full proposal stays on the item.
type DeltaDetail struct {
	DeltaID   string `json:"deltaId"`
	Kind      string `json:"kind"`
	Vertical  string `json:"vertical"`
	PromptID  string `json:"promptId"`
	Rationale string `json:"rationale"`
}

// Deps are injectable for tests.
type Deps struct {
	Fetch    func(ctx context.Context, vertical, sinceRFC string, subjects map[string]bool) ([]FeedbackRow, error)
	Profile  func(ctx context.Context, vertical string) (json.RawMessage, error)
	PutDelta func(ctx context.Context, d TunerDelta) error
	Publish  func(ctx context.Context, det DeltaDetail) error
	Now      func() time.Time
}

// Config is the per-tuner wiring a Lambda's main supplies. Propose is
// the (typed) Bedrock call — built in production by PromptProposer,
// faked in tests.
type Config struct {
	Consumer  string
	Kind      string // style | email_tone | targeting
	PromptID  string
	Verticals []string
	Subjects  []string
	SinceDays int
	// Propose returns the marshalled proposed payload + the model's
	// rationale for one vertical. A cost-cap exhaustion must surface
	// as cost.ErrBudgetCapExceeded so Run can skip the week.
	Propose func(ctx context.Context, vertical, user string) (payload []byte, rationale string, err error)
}

// Run executes one weekly pass for a tuner.
func Run(ctx context.Context, d Deps, cfg Config) error {
	logger := applog.FromContext(ctx)
	now := d.Now().UTC()
	since := now.AddDate(0, 0, -cfg.SinceDays).Format(time.RFC3339)
	subj := map[string]bool{}
	for _, s := range cfg.Subjects {
		subj[s] = true
	}

	proposed, skipped := 0, 0
	for _, vertical := range cfg.Verticals {
		fb, err := d.Fetch(ctx, vertical, since, subj)
		if err != nil {
			return fmt.Errorf("%s: fetch feedback %s: %w", cfg.Consumer, vertical, err)
		}
		if len(fb) == 0 {
			skipped++
			continue
		}
		profile, err := d.Profile(ctx, vertical)
		if err != nil {
			return fmt.Errorf("%s: load profile %s: %w", cfg.Consumer, vertical, err)
		}
		fbJSON, _ := json.Marshal(fb)
		user := fmt.Sprintf(`{"vertical":%q,"currentProfile":%s,"feedbackBatch":%s}`,
			vertical, nonNull(profile), string(fbJSON))

		payload, rationale, err := cfg.Propose(ctx, vertical, user)
		if err != nil {
			if errors.Is(err, cost.ErrBudgetCapExceeded) {
				logger.Warn(cfg.Consumer+".budget_cap_skip_week", "vertical", vertical)
				return nil
			}
			return fmt.Errorf("%s: propose %s: %w", cfg.Consumer, vertical, err)
		}

		id := weekID(now, vertical, cfg.Kind)
		nowRFC := now.Format(time.RFC3339)
		// SK is the stable per-(ISO-week,kind,vertical) id — NOT
		// time-prefixed — so a same-week scheduler retry overwrites
		// the proposal rather than creating a duplicate. gsi1sk keeps
		// the timestamp for newest-first /tuners ordering; a GSI has
		// exactly one entry per base item, so the overwrite replaces
		// it (no duplicate in the pending list either).
		delta := TunerDelta{
			PK:              "DELTA#" + cfg.Kind + "#" + vertical,
			SK:              id,
			Type:            "TunerDelta",
			ID:              id,
			Kind:            cfg.Kind,
			Vertical:        vertical,
			Status:          "pending",
			ProposedPayload: string(payload),
			Rationale:       rationale,
			PromptID:        cfg.PromptID,
			CreatedAt:       nowRFC,
			UpdatedAt:       nowRFC,
			GSI1PK:          "DELTA#STATUS#pending",
			GSI1SK:          nowRFC + "#" + id,
		}
		if err := d.PutDelta(ctx, delta); err != nil {
			return fmt.Errorf("%s: put delta %s: %w", cfg.Consumer, vertical, err)
		}
		if err := d.Publish(ctx, DeltaDetail{
			DeltaID: id, Kind: cfg.Kind, Vertical: vertical,
			PromptID: cfg.PromptID, Rationale: rationale,
		}); err != nil {
			return fmt.Errorf("%s: publish %s: %w", cfg.Consumer, vertical, err)
		}
		proposed++
	}
	logger.Info(cfg.Consumer+".sweep_done", "proposed", proposed, "skipped_no_feedback", skipped)
	return nil
}

func nonNull(r json.RawMessage) string {
	if len(r) == 0 {
		return "null"
	}
	return string(r)
}

// weekID is stable per (ISO-week, kind, vertical) so a same-week retry
// overwrites the proposal rather than duplicating it.
func weekID(now time.Time, vertical, kind string) string {
	y, w := now.ISOWeek()
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-W%02d|%s|%s", y, w, kind, vertical)))
	return hex.EncodeToString(h[:8])
}

// --- production wiring -------------------------------------------------

// PromptProposer builds a production Propose for a typed prompt: it
// looks up the Bedrock budget cap and runs prompts.Invoke (schema +
// PostValidate). The cache key is empty: per
// .ralph/specs/07-bedrock-prompts.md L378 tuner prompts are NOT
// cached (every weekly run must see fresh feedback) — an empty
// cacheKey makes bedrock.InvokeStructured skip the cache entirely.
func PromptProposer[T any](p prompts.Prompt[T], rationale func(T) string) func(context.Context, string, string) ([]byte, string, error) {
	return func(ctx context.Context, vertical, user string) ([]byte, string, error) {
		capUSD, err := killswitch.CapUSD(ctx, killswitch.StageAudit) // DailyBedrockUsd
		if err != nil {
			return nil, "", err
		}
		out, err := prompts.Invoke(ctx, p, []bedrock.Message{{Role: "user", Content: user}}, capUSD, "")
		if err != nil {
			return nil, "", err
		}
		payload, err := json.Marshal(out)
		if err != nil {
			return nil, "", fmt.Errorf("marshal delta: %w", err)
		}
		return payload, rationale(out), nil
	}
}

// BuildDeps wires the real AWS-backed Deps. profileKey returns the
// (pk,sk) of the current profile item for a vertical, or ok=false when
// the kind has no per-vertical profile (targeting).
func BuildDeps(ctx context.Context, consumer string,
	profileKey func(vertical string) (pk, sk string, ok bool)) (Deps, error) {
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return Deps{}, err
	}
	return Deps{
		Fetch:    fetchFeedback,
		Profile:  profileFetcher(profileKey),
		PutDelta: putDelta,
		Publish: func(ctx context.Context, det DeltaDetail) error {
			return pkgevents.Publish(ctx, publisher,
				pkgevents.New("tuner.delta.proposed", consumer, det))
		},
		Now: time.Now,
	}, nil
}

func fetchFeedback(ctx context.Context, vertical, sinceRFC string, subjects map[string]bool) ([]FeedbackRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	var (
		rows    []FeedbackRow
		lastKey map[string]dtypes.AttributeValue
	)
	for {
		out, err := client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(ddb.TableName()),
			KeyConditionExpression: aws.String("pk = :pk AND sk > :since"),
			ExpressionAttributeValues: map[string]dtypes.AttributeValue{
				":pk":    &dtypes.AttributeValueMemberS{Value: "FEEDBACK#" + vertical},
				":since": &dtypes.AttributeValueMemberS{Value: sinceRFC},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query feedback: %w", err)
		}
		for _, it := range out.Items {
			var r FeedbackRow
			if err := attributevalue.UnmarshalMap(it, &r); err != nil {
				return nil, fmt.Errorf("unmarshal feedback: %w", err)
			}
			if len(subjects) == 0 || subjects[r.Subject] {
				rows = append(rows, r)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return rows, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

func profileFetcher(profileKey func(string) (string, string, bool)) func(context.Context, string) (json.RawMessage, error) {
	return func(ctx context.Context, vertical string) (json.RawMessage, error) {
		pk, sk, ok := profileKey(vertical)
		if !ok {
			return nil, nil
		}
		client, err := ddb.Client(ctx)
		if err != nil {
			return nil, err
		}
		out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(ddb.TableName()),
			Key: map[string]dtypes.AttributeValue{
				"pk": &dtypes.AttributeValueMemberS{Value: pk},
				"sk": &dtypes.AttributeValueMemberS{Value: sk},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("get profile %s: %w", pk, err)
		}
		if len(out.Item) == 0 {
			return nil, nil
		}
		var m map[string]any
		if err := attributevalue.UnmarshalMap(out.Item, &m); err != nil {
			return nil, fmt.Errorf("unmarshal profile: %w", err)
		}
		return json.Marshal(m)
	}
}

func putDelta(ctx context.Context, d TunerDelta) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return fmt.Errorf("marshal TunerDelta: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()), Item: item,
	}); err != nil {
		return fmt.Errorf("put TunerDelta: %w", err)
	}
	return nil
}
