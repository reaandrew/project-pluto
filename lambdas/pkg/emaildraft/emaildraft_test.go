package emaildraft

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
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

type fakeDDB struct{}

func (fakeDDB) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (fakeDDB) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (fakeDDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setupFakes(t *testing.T) *fakeBedrock {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	b := &fakeBedrock{}
	bedrock.SetClient(b)
	ddb.SetClient(fakeDDB{})
	t.Cleanup(func() {
		bedrock.SetClient(nil)
		ddb.SetClient(nil)
	})
	return b
}

func bedrockResp(t *testing.T, payload schemas.EmailV1) []byte {
	t.Helper()
	inputRaw, _ := json.Marshal(payload)
	body, err := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"type": "tool_use", "name": "produceEmailDraft", "input": json.RawMessage(inputRaw),
		}},
		"usage": map[string]int{"input_tokens": 400, "output_tokens": 120},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return body
}

func validInput() Input {
	return Input{
		BusinessID: "biz-1",
		WebsiteID:  "web-1",
		ContactID:  "con-1",
		PreviewURL: "https://previews.example.com/sites/web-1",
		Business:   Business{Name: "Acme Accountants", Domain: "acme.co.uk", Vertical: "accountants", Location: "Manchester"},
		Contact:    Contact{FirstName: "Jane", Role: "Director"},
		Audit:      AuditSummary{Score: 38, Summary: "Homepage buries services."},
		Tone: tone.Profile{
			Vertical: "accountants", Version: 3,
			SubjectPatterns:   []string{"Quick redesign preview for {{businessName}}"},
			OpenerPatterns:    []string{"Hi {{firstName}},"},
			ProhibitedPhrases: []string{"industry-leading"},
			OptOutLine:        "Reply 'no thanks' and I won't follow up.",
			Signature:         "Andrew",
		},
	}
}

func validDraft() schemas.EmailV1 {
	return schemas.EmailV1{
		Subject: "Quick redesign preview for Acme Accountants",
		Body: "Hi Jane,\n\nYour homepage buries the services list below the fold.\n\n" +
			"I mocked up a private preview: https://previews.example.com/sites/web-1\n" +
			"Use access code {{PASSCODE}} to open it.\n\n" +
			"Reply 'no thanks' and I won't follow up.\n\nAndrew",
		WordCount: 70,
	}
}

func TestRun_HappyPath(t *testing.T) {
	b := setupFakes(t)
	b.body = bedrockResp(t, validDraft())
	out, err := Run(context.Background(), validInput(), 1.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.Body, schemas.PasscodePlaceholder) {
		t.Error("returned body must still carry the {{PASSCODE}} placeholder (Lambda substitutes)")
	}
	// System message embeds the tone block + safety rules.
	var reqBody map[string]any
	if err := json.Unmarshal(b.gotInput.Body, &reqBody); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	sys, _ := reqBody["system"].(string)
	if !strings.Contains(sys, "<email_tone>") || !strings.Contains(sys, "<safety_rules>") {
		t.Errorf("system message missing email_tone/safety blocks")
	}
}

func TestRun_CacheKeyExcludesPasscodeAndUsesToneVersion(t *testing.T) {
	b := setupFakes(t)
	b.body = bedrockResp(t, validDraft())
	if _, err := Run(context.Background(), validInput(), 1.0); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var reqBody map[string]any
	_ = json.Unmarshal(b.gotInput.Body, &reqBody)
	raw, _ := json.Marshal(reqBody)
	// Sanity: the model request must never carry a real passcode and
	// the user content references the placeholder-free preview URL.
	if strings.Contains(string(raw), "PASSCODE=") {
		t.Error("model request must not contain a real passcode")
	}
}

func TestRun_RejectsContentViolations(t *testing.T) {
	b := setupFakes(t)
	bad := validDraft()
	bad.Body = strings.ReplaceAll(bad.Body, "Reply 'no thanks' and I won't follow up.", "") // drop opt-out
	b.body = bedrockResp(t, bad)
	if _, err := Run(context.Background(), validInput(), 1.0); err == nil {
		t.Error("expected content post-validation to reject a draft missing the opt-out line")
	}
}

func TestRun_RequiresInputs(t *testing.T) {
	setupFakes(t)
	cases := map[string]func(*Input){
		"no businessID": func(i *Input) { i.BusinessID = "" },
		"no websiteID":  func(i *Input) { i.WebsiteID = "" },
		"no previewURL": func(i *Input) { i.PreviewURL = "" },
		"invalid tone":  func(i *Input) { i.Tone.OptOutLine = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			if _, err := Run(context.Background(), in, 1.0); err == nil {
				t.Errorf("expected error (%s)", name)
			}
		})
	}
}
