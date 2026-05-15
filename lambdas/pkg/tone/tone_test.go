package tone

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// In-memory fake of the DDB surface tone uses (GetItem/PutItem/Scan).
type fakeDDB struct {
	items map[string]map[string]dtypes.AttributeValue
}

func newFake() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]dtypes.AttributeValue{}}
}
func key(pk string) string { return pk + "|PROFILE" }

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	pk := in.Item["pk"].(*dtypes.AttributeValueMemberS).Value
	f.items[key(pk)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	return &dynamodb.GetItemOutput{Item: f.items[key(pk)]}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	out := &dynamodb.ScanOutput{}
	for _, it := range f.items {
		out.Items = append(out.Items, it)
	}
	return out, nil
}
func (f *fakeDDB) UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := newFake()
	ddb.SetClient(f)
	SetNowFunc(func() time.Time { return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC) })
	etagN := 0
	SetEtagFunc(func(int) string { etagN++; return "etag-" + string(rune('0'+etagN)) })
	t.Cleanup(func() {
		ddb.SetClient(nil)
		SetNowFunc(func() time.Time { return time.Now().UTC() })
		SetEtagFunc(randomHex)
	})
	return f
}

func sample() Profile {
	return Profile{
		Vertical:          "accountants",
		SubjectPatterns:   []string{"Quick redesign preview for {{businessName}}"},
		OpenerPatterns:    []string{"Hi {{firstName}},"},
		ProhibitedPhrases: []string{"password", "industry-leading"},
		Signature:         "Andrew\nAndrew Rea Associates",
		OptOutLine:        "Reply 'no thanks' and I won't follow up.",
		Version:           1,
	}
}

func TestValidate_RequiresFields(t *testing.T) {
	cases := map[string]func(*Profile){
		"empty vertical":     func(p *Profile) { p.Vertical = "" },
		"empty optOutLine":   func(p *Profile) { p.OptOutLine = "" },
		"no subjectPatterns": func(p *Profile) { p.SubjectPatterns = nil },
		"no openerPatterns":  func(p *Profile) { p.OpenerPatterns = nil },
		"version zero":       func(p *Profile) { p.Version = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := sample()
			mutate(&p)
			if err := p.Validate(); err == nil {
				t.Errorf("Validate accepted invalid profile (%s)", name)
			}
		})
	}
}

func TestValidate_AcceptsGood(t *testing.T) {
	if err := sample().Validate(); err != nil {
		t.Errorf("Validate rejected a good profile: %v", err)
	}
}

func TestPutThenGet_RoundTrip(t *testing.T) {
	setup(t)
	put, err := Put(context.Background(), sample())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if put.CreatedAt == "" || put.UpdatedAt == "" || put.Etag == "" {
		t.Errorf("Put did not fill timestamps/etag: %+v", put)
	}
	got, err := Get(context.Background(), "accountants")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OptOutLine != sample().OptOutLine || len(got.SubjectPatterns) != 1 {
		t.Errorf("round-trip drift: %+v", got)
	}
}

func TestPut_LowercasesPK(t *testing.T) {
	f := setup(t)
	p := sample()
	p.Vertical = "Accountants"
	if _, err := Put(context.Background(), p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := f.items[key("EMAIL_TONE#accountants")]; !ok {
		t.Errorf("pk not lowercased; keys=%v", f.items)
	}
}

func TestPut_RejectsInvalid(t *testing.T) {
	setup(t)
	p := sample()
	p.OptOutLine = ""
	_, err := Put(context.Background(), p)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	setup(t)
	_, err := Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetOrDefault_PrefersVerticalSpecific(t *testing.T) {
	setup(t)
	def := sample()
	def.Vertical = "default"
	def.OptOutLine = "default-optout"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	got, err := GetOrDefault(context.Background(), "accountants")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vertical != "accountants" {
		t.Errorf("want vertical-specific, got %q", got.Vertical)
	}
}

func TestGetOrDefault_FallsBackToDefault(t *testing.T) {
	setup(t)
	def := sample()
	def.Vertical = "default"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	got, err := GetOrDefault(context.Background(), "plumbers")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vertical != "default" {
		t.Errorf("want default fallback, got %q", got.Vertical)
	}
}

func TestGetOrDefault_EmptyVerticalUsesDefault(t *testing.T) {
	setup(t)
	def := sample()
	def.Vertical = "default"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if _, err := GetOrDefault(context.Background(), ""); err != nil {
		t.Errorf("empty vertical should resolve to default: %v", err)
	}
}

func TestUpdate_EtagMismatchRejects(t *testing.T) {
	setup(t)
	if _, err := Put(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	_, err := Update(context.Background(), "accountants", sample(), "wrong-etag")
	if !errors.Is(err, ErrEtagMismatch) {
		t.Errorf("want ErrEtagMismatch, got %v", err)
	}
}

func TestUpdate_AutoBumpsVersion(t *testing.T) {
	setup(t)
	put, _ := Put(context.Background(), sample())
	next := sample()
	next.Version = 1 // caller forgot to bump
	upd, err := Update(context.Background(), "accountants", next, put.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Version != 2 {
		t.Errorf("want auto-bumped version 2, got %d", upd.Version)
	}
}

func TestUpdate_RespectsCallerBumpedVersion(t *testing.T) {
	setup(t)
	put, _ := Put(context.Background(), sample())
	next := sample()
	next.Version = 9
	upd, err := Update(context.Background(), "accountants", next, put.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Version != 9 {
		t.Errorf("want caller version 9, got %d", upd.Version)
	}
}

func TestUpdate_RotatesEtagPreservesCreatedAt(t *testing.T) {
	setup(t)
	put, _ := Put(context.Background(), sample())
	upd, err := Update(context.Background(), "accountants", sample(), put.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Etag == put.Etag {
		t.Error("etag must rotate on Update")
	}
	if upd.CreatedAt != put.CreatedAt {
		t.Errorf("CreatedAt must be preserved: %q != %q", upd.CreatedAt, put.CreatedAt)
	}
}

func TestList_FiltersByType(t *testing.T) {
	setup(t)
	a := sample()
	b := sample()
	b.Vertical = "default"
	if _, err := Put(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, err := List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 profiles, got %d", len(got))
	}
}
