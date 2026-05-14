package spec

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

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
)

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
	getKeys []map[string]dtypes.AttributeValue
	getOut  *dynamodb.GetItemOutput
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

func makeBedrockResponse(t *testing.T, toolName string, payload schemas.SpecV1, inTok, outTok int) []byte {
	t.Helper()
	inputRaw, err := json.Marshal(payload)
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

func validInput() Input {
	return Input{
		BusinessID: "biz-1",
		AuditID:    "audit-1",
		Business: Business{
			Name: "Acme Plumbing", Domain: "acme.co.uk",
			Vertical: "trades", Location: "Manchester",
		},
		AuditSummary: AuditSummary{
			Score: 35, Summary: "Mobile broken; weak CTA.",
			IssueTypes: []string{"mobile", "conversion"},
		},
		StyleGuide: style.Guide{
			Vertical: "trades", Tone: "plain-English",
			DoPhrases: []string{"24/7"}, DontPhrases: []string{"industry-leading"},
			Palette: style.Palette{Primary: "#000", Neutral: []string{"#fff"}},
			Version: 2,
		},
		ExtractedContent: ExtractedContent{
			Services: []string{"Boiler repair", "Bathroom installation"},
			Phone:    "0161 234 5678",
		},
	}
}

func validSpec() schemas.SpecV1 {
	return schemas.SpecV1{
		Brand: schemas.SpecBrand{
			Tone: "plain", Positioning: "Local plumbers, Manchester.",
			Palette: schemas.SpecPalette{Primary: "#0F4C81", NeutralDark: "#000", NeutralLight: "#fff"},
		},
		Page: schemas.SpecPage{
			Sections: []schemas.SpecSection{
				{Type: schemas.SectionHero, Headline: "Hi", Subheadline: "Hello",
					PrimaryCta: &schemas.SpecCTA{Label: "Call", Action: "call"}},
				{Type: schemas.SectionServices, Title: "What we do",
					Items: []schemas.SpecSubItem{
						{Name: "a", OneLine: "b"}, {Name: "c", OneLine: "d"}, {Name: "e", OneLine: "f"},
					}},
				{Type: schemas.SectionAbout, Paragraph: "About us."},
				{Type: schemas.SectionContact, Phone: "0161 234 5678"},
			},
		},
		SEO: schemas.SpecSEO{Title: "Acme", Description: "Acme Plumbers."},
		Constraints: schemas.SpecConstraints{
			DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
		},
	}
}

func TestRun_HappyPath(t *testing.T) {
	b, _ := setupFakes(t)
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, validSpec(), 1000, 500)

	got, err := Run(context.Background(), validInput(), 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Page.Sections) != 4 {
		t.Errorf("section count drift: %d", len(got.Page.Sections))
	}
	if !got.Constraints.DoNotInventTestimonials {
		t.Error("constraints flag drift")
	}
	if b.calls != 1 {
		t.Errorf("Bedrock called %d times, want 1", b.calls)
	}
	// Decode the body so JSON-escaped angle brackets (<, >)
	// round-trip into real "<" / ">" characters before substring matching.
	var sent struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal(b.gotInput.Body, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if !strings.Contains(sent.System, "<style_guide>") {
		t.Errorf("system message should contain <style_guide> block: %s", sent.System)
	}
	if !strings.Contains(sent.System, `"vertical":"trades"`) {
		t.Errorf("style guide JSON not in system message: %s", sent.System)
	}
}

func TestRun_CacheKeyDerivedFromBizAuditStyleVersion(t *testing.T) {
	b, d := setupFakes(t)
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, validSpec(), 100, 50)

	in := validInput()
	if _, err := Run(context.Background(), in, 5.0); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantHash := prompts.HashInputs(in.BusinessID, in.AuditID, "2")
	wantKey := bedrock.CacheKey(prompts.SpecV1.ID, wantHash)
	pk, sk, ok := d.firstCacheLookup()
	if !ok {
		t.Fatalf("no cache lookup performed; got keys: %+v", d.getKeys)
	}
	if !strings.Contains(sk, wantKey) {
		t.Errorf("cache lookup sk=%q does not contain expected %q", sk, wantKey)
	}
	if !strings.Contains(pk, prompts.SpecV1.ID) {
		t.Errorf("cache lookup pk=%q does not reference prompt ID", pk)
	}
}

func TestRun_DifferentStyleVersionsProduceDifferentCacheKeys(t *testing.T) {
	v1 := bedrock.CacheKey(prompts.SpecV1.ID, prompts.HashInputs("biz", "audit", "1"))
	v2 := bedrock.CacheKey(prompts.SpecV1.ID, prompts.HashInputs("biz", "audit", "2"))
	if v1 == v2 {
		t.Error("style guide version must change the cache key")
	}
}

func TestRun_CacheHitSkipsBedrock(t *testing.T) {
	b, d := setupFakes(t)
	cached := validSpec()
	cachedRaw, _ := json.Marshal(cached)
	d.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"payload": &dtypes.AttributeValueMemberS{Value: string(cachedRaw)},
		},
	}

	got, err := Run(context.Background(), validInput(), 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.calls != 0 {
		t.Errorf("Bedrock should not be called on cache hit (got %d)", b.calls)
	}
	if len(got.Page.Sections) != len(cached.Page.Sections) {
		t.Errorf("cached payload not returned")
	}
}

func TestRun_RequiresBusinessAndAuditAndStyleGuide(t *testing.T) {
	setupFakes(t)
	in := validInput()
	in.BusinessID = ""
	if _, err := Run(context.Background(), in, 5.0); err == nil {
		t.Error("expected error on empty BusinessID")
	}
	in = validInput()
	in.AuditID = ""
	if _, err := Run(context.Background(), in, 5.0); err == nil {
		t.Error("expected error on empty AuditID")
	}
	in = validInput()
	in.StyleGuide.Tone = ""
	if _, err := Run(context.Background(), in, 5.0); err == nil {
		t.Error("expected error on invalid style guide")
	}
}

func TestComposeSystem(t *testing.T) {
	g := style.Guide{
		Vertical: "trades", Tone: "plain", Version: 1,
		Palette: style.Palette{Primary: "#000", Neutral: []string{"#fff"}},
	}
	s := composeSystem(g)
	if !strings.Contains(s, prompts.SpecSystemPrefix) {
		t.Error("system should contain the spec system prefix")
	}
	if !strings.Contains(s, "<style_guide>") || !strings.Contains(s, "</style_guide>") {
		t.Errorf("system should contain <style_guide> block: %s", s)
	}
	if !strings.Contains(s, prompts.SafetyRulesBlock) {
		t.Error("system should contain SafetyRulesBlock (transitively, via the prefix)")
	}
}

func TestRun_BedrockErrorSurfaces(t *testing.T) {
	b, _ := setupFakes(t)
	b.err = errors.New("bedrock down")
	if _, err := Run(context.Background(), validInput(), 5.0); err == nil {
		t.Fatal("expected bedrock error to surface")
	}
}

func TestRun_TestimonialResponseRejectedByPostValidator(t *testing.T) {
	b, _ := setupFakes(t)
	bad := validSpec()
	bad.Page.Sections[0] = schemas.SpecSection{
		Type: schemas.SectionAbout, Paragraph: "Customer testimonial: great service.",
	}
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, bad, 100, 50)
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected post-validator to reject testimonial-shaped section")
	}
}

// End-to-end adversarial coverage at the Run level — confirms the
// PostValidate hook on Prompt[T] fires through prompts.Invoke, not
// just at the schemas package layer. If a future refactor silently
// dropped post-validation, these tests would fail.

func TestRun_ConstraintsFlagFalse_RejectedByPostValidator(t *testing.T) {
	b, _ := setupFakes(t)
	bad := validSpec()
	bad.Constraints.DoNotInventAwards = false
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, bad, 100, 50)
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected post-validator to reject constraints flag false")
	}
}

func TestRun_TooFewSections_RejectedByPostValidator(t *testing.T) {
	b, _ := setupFakes(t)
	bad := validSpec()
	bad.Page.Sections = bad.Page.Sections[:3]
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, bad, 100, 50)
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected post-validator to reject sections count < 4")
	}
}

func TestRun_PasswordInCopy_RejectedByPostValidator(t *testing.T) {
	b, _ := setupFakes(t)
	bad := validSpec()
	bad.Page.Sections[0].Headline = "Forgot your password?"
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, bad, 100, 50)
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected post-validator to reject 'password' in user-facing copy")
	}
}

func TestRun_UnknownSectionType_RejectedByPostValidator(t *testing.T) {
	b, _ := setupFakes(t)
	bad := validSpec()
	bad.Page.Sections[0].Type = "newsletter"
	b.body = makeBedrockResponse(t, prompts.SpecV1.ToolName, bad, 100, 50)
	_, err := Run(context.Background(), validInput(), 5.0)
	if err == nil {
		t.Fatal("expected post-validator to reject unknown section type")
	}
}

func TestPromptCacheTTLIsNinetyDays(t *testing.T) {
	if prompts.SpecV1.CacheTTL != 90*24*time.Hour {
		t.Errorf("SpecV1.CacheTTL = %v, want 90 days", prompts.SpecV1.CacheTTL)
	}
}
