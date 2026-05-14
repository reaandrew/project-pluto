package style

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- in-memory DDB fake -------------------------------------------------

type fakeDDB struct {
	items map[string]map[string]dtypes.AttributeValue
}

func newFakeDDB() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]dtypes.AttributeValue{}}
}

func keyOf(pk, sk string) string { return pk + "|" + sk }

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	pk := in.Item["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Item["sk"].(*dtypes.AttributeValueMemberS).Value
	f.items[keyOf(pk, sk)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk := in.Key["pk"].(*dtypes.AttributeValueMemberS).Value
	sk := in.Key["sk"].(*dtypes.AttributeValueMemberS).Value
	item := f.items[keyOf(pk, sk)]
	return &dynamodb.GetItemOutput{Item: item}, nil
}
func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	items := make([]map[string]dtypes.AttributeValue, 0, len(f.items))
	for _, v := range f.items {
		if t, ok := v["type"].(*dtypes.AttributeValueMemberS); ok && t.Value == "VerticalStyleGuide" {
			items = append(items, v)
		}
	}
	return &dynamodb.ScanOutput{Items: items}, nil
}
func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func setup(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := newFakeDDB()
	ddb.SetClient(f)
	SetNowFunc(func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) })
	count := 0
	SetEtagFunc(func(_ int) string {
		count++
		return "etag-" + strconvN(count)
	})
	t.Cleanup(func() {
		ddb.SetClient(nil)
		SetNowFunc(func() time.Time { return time.Now().UTC() })
		SetEtagFunc(randomHex)
	})
	return f
}

func strconvN(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func sampleGuide() Guide {
	return Guide{
		Vertical:     "accountants",
		Tone:         "professional, plain-English",
		DoPhrases:    []string{"clear pricing"},
		DontPhrases:  []string{"leverage synergies"},
		AntiPatterns: []string{"stock handshake"},
		Palette:      Palette{Primary: "#0F4C81", Neutral: []string{"#111", "#666", "#eee"}},
		Version:      1,
	}
}

// --- Validate ----

func TestValidate_RequiresFields(t *testing.T) {
	cases := map[string]Guide{
		"empty vertical": {Tone: "x", Version: 1, Palette: Palette{Primary: "#000", Neutral: []string{"#fff"}}},
		"empty tone":     {Vertical: "x", Version: 1, Palette: Palette{Primary: "#000", Neutral: []string{"#fff"}}},
		"version 0":      {Vertical: "x", Tone: "y", Version: 0, Palette: Palette{Primary: "#000", Neutral: []string{"#fff"}}},
		"no primary":     {Vertical: "x", Tone: "y", Version: 1, Palette: Palette{Neutral: []string{"#fff"}}},
		"no neutral":     {Vertical: "x", Tone: "y", Version: 1, Palette: Palette{Primary: "#000"}},
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			if err := g.Validate(); err == nil {
				t.Errorf("expected validation error for %q", name)
			}
		})
	}
}

func TestValidate_AcceptsGoodGuide(t *testing.T) {
	if err := sampleGuide().Validate(); err != nil {
		t.Errorf("good guide rejected: %v", err)
	}
}

// --- Put then Get ----

func TestPutThenGet_RoundTrip(t *testing.T) {
	setup(t)
	g, err := Put(context.Background(), sampleGuide())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if g.Etag == "" || g.CreatedAt == "" || g.UpdatedAt == "" {
		t.Errorf("Put didn't fill timestamps/etag: %+v", g)
	}

	got, err := Get(context.Background(), "accountants")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Vertical != "accountants" || got.Version != 1 || got.Etag != g.Etag {
		t.Errorf("round-trip drift: %+v", got)
	}
}

func TestPut_LowercasesPK(t *testing.T) {
	f := setup(t)
	in := sampleGuide()
	in.Vertical = "ACCOUNTANTS"
	if _, err := Put(context.Background(), in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Row stored under lowercase pk.
	for k := range f.items {
		if !strings.HasPrefix(k, "STYLE#accountants") {
			t.Errorf("expected lowercased pk, key=%q", k)
		}
	}
}

func TestPut_RejectsInvalid(t *testing.T) {
	setup(t)
	in := sampleGuide()
	in.Tone = ""
	if _, err := Put(context.Background(), in); err == nil || !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid, got %v", err)
	}
}

// --- Get ----

func TestGet_NotFound(t *testing.T) {
	setup(t)
	_, err := Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// --- GetOrDefault ----

func TestGetOrDefault_PrefersVerticalSpecific(t *testing.T) {
	setup(t)
	def := sampleGuide()
	def.Vertical = "default"
	def.Tone = "default tone"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatalf("Put default: %v", err)
	}
	specific := sampleGuide()
	specific.Tone = "vertical tone"
	if _, err := Put(context.Background(), specific); err != nil {
		t.Fatalf("Put specific: %v", err)
	}
	got, err := GetOrDefault(context.Background(), "accountants")
	if err != nil {
		t.Fatalf("GetOrDefault: %v", err)
	}
	if got.Tone != "vertical tone" {
		t.Errorf("expected vertical-specific guide, got %+v", got)
	}
}

func TestGetOrDefault_FallsBackToDefault(t *testing.T) {
	setup(t)
	def := sampleGuide()
	def.Vertical = "default"
	def.Tone = "default tone"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatalf("Put default: %v", err)
	}
	got, err := GetOrDefault(context.Background(), "tradespeople")
	if err != nil {
		t.Fatalf("GetOrDefault: %v", err)
	}
	if got.Tone != "default tone" {
		t.Errorf("expected default fallback, got %+v", got)
	}
}

func TestGetOrDefault_EmptyVerticalUsesDefault(t *testing.T) {
	setup(t)
	def := sampleGuide()
	def.Vertical = "default"
	def.Tone = "default tone"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatalf("Put default: %v", err)
	}
	got, err := GetOrDefault(context.Background(), "")
	if err != nil {
		t.Fatalf("GetOrDefault: %v", err)
	}
	if got.Tone != "default tone" {
		t.Errorf("expected default fallback, got %+v", got)
	}
}

// --- Update + etag + version bump ----

func TestUpdate_EtagMismatchRejects(t *testing.T) {
	setup(t)
	saved, err := Put(context.Background(), sampleGuide())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	saved.Tone = "new tone"
	_, err = Update(context.Background(), "accountants", saved, "WRONG-ETAG")
	if !errors.Is(err, ErrEtagMismatch) {
		t.Errorf("expected ErrEtagMismatch, got %v", err)
	}
}

func TestUpdate_AutoBumpsVersion(t *testing.T) {
	setup(t)
	saved, err := Put(context.Background(), sampleGuide())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Caller forgets to bump version.
	updated := saved
	updated.Tone = "v2 tone"
	updated.Version = saved.Version // unchanged
	got, err := Update(context.Background(), "accountants", updated, saved.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Version != saved.Version+1 {
		t.Errorf("Version not auto-bumped: %d", got.Version)
	}
}

func TestUpdate_RespectsCallerBumpedVersion(t *testing.T) {
	setup(t)
	saved, _ := Put(context.Background(), sampleGuide())
	updated := saved
	updated.Tone = "v2 tone"
	updated.Version = saved.Version + 5
	got, err := Update(context.Background(), "accountants", updated, saved.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Version != saved.Version+5 {
		t.Errorf("Version override not respected: %d", got.Version)
	}
}

func TestUpdate_RotatesEtag(t *testing.T) {
	setup(t)
	saved, _ := Put(context.Background(), sampleGuide())
	updated := saved
	updated.Tone = "v2"
	got, err := Update(context.Background(), "accountants", updated, saved.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Etag == saved.Etag {
		t.Errorf("etag should rotate on update")
	}
	if got.CreatedAt != saved.CreatedAt {
		t.Errorf("createdAt should not change on update")
	}
}

// --- List ----

func TestList_ReturnsAllGuides(t *testing.T) {
	setup(t)
	def := sampleGuide()
	def.Vertical = "default"
	if _, err := Put(context.Background(), def); err != nil {
		t.Fatalf("Put default: %v", err)
	}
	if _, err := Put(context.Background(), sampleGuide()); err != nil {
		t.Fatalf("Put accountants: %v", err)
	}
	got, err := List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 guides, got %d", len(got))
	}
}

// --- DDB unmarshal sanity (catches schema drift) ----

func TestRowShape_RoundTripsThroughAttributeValue(t *testing.T) {
	in := sampleGuide()
	in.CreatedAt = "2026-05-14T12:00:00Z"
	in.UpdatedAt = "2026-05-14T12:00:00Z"
	in.Etag = "abc"
	m, err := attributevalue.MarshalMap(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Guide
	if err := attributevalue.UnmarshalMap(m, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Tone != in.Tone || len(out.DoPhrases) != len(in.DoPhrases) || out.Palette.Primary != in.Palette.Primary {
		t.Errorf("round-trip drift: %+v vs %+v", out, in)
	}
}
