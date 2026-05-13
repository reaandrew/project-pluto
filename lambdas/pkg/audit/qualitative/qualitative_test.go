package qualitative

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/technical"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// --- fakes ----------------------------------------------------------------

type fakeBedrock struct {
	gotInput *bedrockruntime.InvokeModelInput
	body     []byte
	err      error
	calls    int
}

func (f *fakeBedrock) InvokeModel(_ context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.calls++
	f.gotInput = in
	if f.err != nil {
		return nil, f.err
	}
	return &bedrockruntime.InvokeModelOutput{Body: f.body}, nil
}

type fakeDDB struct {
	getOut  *dynamodb.GetItemOutput
	getKeys []map[string]dtypes.AttributeValue
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.getKeys = append(f.getKeys, in.Key)
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

// firstCacheLookup returns the (pk, sk) of the first GetItem call that
// matches the cache shape (pk starting with "CACHE#"). cost.Assert
// interleaves a CAP# GetItem alongside the cache lookup; this filter
// pulls out just the cache one for assertion purposes.
func (f *fakeDDB) firstCacheLookup() (pk, sk string, ok bool) {
	for _, k := range f.getKeys {
		gpk, _ := k["pk"].(*dtypes.AttributeValueMemberS)
		gsk, _ := k["sk"].(*dtypes.AttributeValueMemberS)
		if gpk != nil && strings.HasPrefix(gpk.Value, "CACHE#") {
			return gpk.Value, gsk.Value, true
		}
	}
	return "", "", false
}
func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setupFakes(t *testing.T) (*fakeBedrock, *fakeDDB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	b := &fakeBedrock{}
	d := &fakeDDB{}
	bedrock.SetClient(b)
	ddb.SetClient(d)
	t.Cleanup(func() {
		bedrock.SetClient(nil)
		ddb.SetClient(nil)
	})
	return b, d
}

// makeBedrockResponse builds an Anthropic-on-Bedrock tool-use response body.
func makeBedrockResponse(t *testing.T, toolName string, toolInput any, inTok, outTok int) []byte {
	t.Helper()
	inputRaw, err := json.Marshal(toolInput)
	if err != nil {
		t.Fatalf("marshal toolInput: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"type":  "tool_use",
			"name":  toolName,
			"input": json.RawMessage(inputRaw),
		}},
		"usage": map[string]int{
			"input_tokens":  inTok,
			"output_tokens": outTok,
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return body
}

// validInput returns a realistic Input for happy-path tests.
func validInput() Input {
	return Input{
		Domain: "acme.co.uk",
		Business: Business{
			Name:     "Acme Plumbing",
			Vertical: "trades",
			Location: "Manchester, UK",
		},
		Technical: technical.Result{
			HTTPS: true, Viewport: false, Favicon: true, ContactDetected: false,
			Lighthouse:   technical.Lighthouse{Performance: 35, Accessibility: 60, SEO: 50},
			HomepageHash: "deadbeef",
		},
		HTMLExcerpt: "Acme Plumbing — Manchester. Call us today.",
	}
}

// --- Run: happy path ------------------------------------------------------

func TestRun_HappyPathReturnsParsedAuditAndInvokesBedrock(t *testing.T) {
	bedrockFake, _ := setupFakes(t)
	want := schemas.AuditV1{
		Score: 35, WorthRedesigning: true,
		Summary: "Mobile broken; weak CTA; slow.",
		Issues: []schemas.AuditIssue{
			{Type: "mobile", Severity: "high", Description: "Viewport missing."},
			{Type: "conversion", Severity: "medium", Description: "No phone above the fold."},
		},
	}
	bedrockFake.body = makeBedrockResponse(t, prompts.AuditQualitativeV1.ToolName, want, 800, 200)

	got, err := Run(context.Background(), validInput(), 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Score != want.Score || got.WorthRedesigning != want.WorthRedesigning || got.Summary != want.Summary {
		t.Errorf("output drift: got %+v, want %+v", got, want)
	}
	if len(got.Issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(got.Issues))
	}
	if bedrockFake.calls != 1 {
		t.Errorf("InvokeModel called %d times, want 1", bedrockFake.calls)
	}
}

// --- Run: cache key + cache hit ------------------------------------------

func TestRun_CacheHitSkipsBedrock(t *testing.T) {
	bedrockFake, ddbFake := setupFakes(t)
	cached := schemas.AuditV1{
		Score: 80, WorthRedesigning: false,
		Summary: "Looks fine.",
		Issues:  []schemas.AuditIssue{{Type: "design", Severity: "low", Description: "minor"}},
	}
	cachedRaw, _ := json.Marshal(cached)
	ddbFake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"payload": &dtypes.AttributeValueMemberS{Value: string(cachedRaw)},
		},
	}

	got, err := Run(context.Background(), validInput(), 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Score != 80 {
		t.Errorf("expected cached payload, got %+v", got)
	}
	if bedrockFake.calls != 0 {
		t.Errorf("Bedrock should not be called on cache hit (got %d)", bedrockFake.calls)
	}
}

// --- Run: cache key derivation ----------------------------------------

func TestRun_CacheKeyDerivedFromDomainAndExcerpt(t *testing.T) {
	bedrockFake, ddbFake := setupFakes(t)
	bedrockFake.body = makeBedrockResponse(t, prompts.AuditQualitativeV1.ToolName, schemas.AuditV1{
		Score: 50, Summary: "ok",
		Issues: []schemas.AuditIssue{{Type: "design", Severity: "low", Description: "x"}},
	}, 100, 50)

	in := validInput()
	if _, err := Run(context.Background(), in, 5.0); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The cache-lookup GetItem must use a key derived from (domain, excerpt).
	wantInputHash := prompts.HashInputs(in.Domain, in.HTMLExcerpt)
	wantCacheKey := bedrock.CacheKey(prompts.AuditQualitativeV1.ID, wantInputHash)
	gotPK, gotSK, ok := ddbFake.firstCacheLookup()
	if !ok {
		t.Fatalf("no cache lookup performed; got keys: %+v", ddbFake.getKeys)
	}
	if !strings.Contains(gotSK, wantCacheKey) {
		t.Errorf("cache lookup sk=%q does not contain expected cacheKey=%q", gotSK, wantCacheKey)
	}
	if !strings.Contains(gotPK, prompts.AuditQualitativeV1.ID) {
		t.Errorf("cache lookup pk=%q does not reference promptID", gotPK)
	}
}

func TestRun_DifferentExcerptsProduceDifferentCacheKeys(t *testing.T) {
	keys := map[string]string{}
	for label, excerpt := range map[string]string{
		"original": "Acme Plumbing — Manchester. Call us.",
		"changed":  "Acme Plumbing — Manchester. Email us.",
	} {
		input := validInput()
		input.HTMLExcerpt = excerpt
		keys[label] = bedrock.CacheKey(
			prompts.AuditQualitativeV1.ID,
			prompts.HashInputs(input.Domain, input.HTMLExcerpt),
		)
	}
	if keys["original"] == keys["changed"] {
		t.Error("different excerpts should produce different cache keys")
	}
}

// --- Run: truncation ------------------------------------------------------

func TestRun_TruncatesHTMLExcerptTo8KBBeforeHashing(t *testing.T) {
	bedrockFake, ddbFake := setupFakes(t)
	bedrockFake.body = makeBedrockResponse(t, prompts.AuditQualitativeV1.ToolName, schemas.AuditV1{
		Score: 50, Summary: "ok",
		Issues: []schemas.AuditIssue{{Type: "design", Severity: "low", Description: "x"}},
	}, 100, 50)

	in := validInput()
	in.HTMLExcerpt = strings.Repeat("a", MaxHTMLExcerptBytes+1024) // 9KB

	if _, err := Run(context.Background(), in, 5.0); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Re-derive what the cache key should be IF Run truncated correctly.
	wantInputHash := prompts.HashInputs(in.Domain, strings.Repeat("a", MaxHTMLExcerptBytes))
	wantCacheKey := bedrock.CacheKey(prompts.AuditQualitativeV1.ID, wantInputHash)
	_, gotSK, ok := ddbFake.firstCacheLookup()
	if !ok {
		t.Fatalf("no cache lookup performed; got keys: %+v", ddbFake.getKeys)
	}
	if !strings.Contains(gotSK, wantCacheKey) {
		t.Errorf("Run did not truncate excerpt before hashing (sk=%q vs wantKey=%q)", gotSK, wantCacheKey)
	}

	// The user message sent to Bedrock should also be truncated — peek at it.
	body := string(bedrockFake.gotInput.Body)
	if strings.Count(body, "aaaa") == 0 {
		t.Error("expected excerpt to appear in Bedrock body")
	}
	if len(body) > MaxHTMLExcerptBytes*2 {
		// Body has system + technical + business overhead; truncated excerpt
		// should keep this well under 2× excerpt cap.
		t.Errorf("bedrock body is suspiciously large (%d bytes); excerpt likely not truncated", len(body))
	}
}

// --- Run: input validation -------------------------------------------------

func TestRun_RequiresDomain(t *testing.T) {
	setupFakes(t)
	in := validInput()
	in.Domain = ""
	if _, err := Run(context.Background(), in, 5.0); err == nil {
		t.Error("Run with empty Domain should error")
	}
}

// --- Run: surfaces underlying errors --------------------------------------

func TestRun_SurfacesBedrockError(t *testing.T) {
	bedrockFake, _ := setupFakes(t)
	bedrockFake.err = errors.New("bedrock down")
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected bedrock error to surface")
	}
}

// --- buildUserMessage / truncation unit tests ----------------------------

func TestBuildUserMessageContainsAllInputBlocks(t *testing.T) {
	in := validInput()
	msg, err := buildUserMessage(in, in.HTMLExcerpt)
	if err != nil {
		t.Fatalf("buildUserMessage: %v", err)
	}
	for _, want := range []string{
		"<business>", "</business>", in.Business.Name, in.Business.Vertical, in.Business.Location,
		"<technical>", "</technical>", "homepageHash",
		"<html_excerpt>", "</html_excerpt>", in.HTMLExcerpt,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("user message missing %q\n%s", want, msg)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := truncateBytes(c.in, c.n); got != c.want {
			t.Errorf("truncateBytes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// --- sanity: the prompt-config cache TTL is what 07-bedrock-prompts.md says
//   (this lives here because the qualitative wrapper is the only thing
//    that actually relies on the 30-day TTL).

func TestPromptCacheTTLIsThirtyDays(t *testing.T) {
	if prompts.AuditQualitativeV1.CacheTTL != 30*24*time.Hour {
		t.Errorf("AuditQualitativeV1.CacheTTL = %v, want 30 days (spec value)",
			prompts.AuditQualitativeV1.CacheTTL)
	}
}
