package targeting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- fake DDB ----------------------------------------------------------

type fakeDDB struct {
	getItem    map[string]map[string]dtypes.AttributeValue // pk -> item
	getErr     error
	putErr     error
	putInputs  []*dynamodb.PutItemInput
	delInputs  []*dynamodb.DeleteItemInput
	delMissing bool
	scanItems  []map[string]dtypes.AttributeValue
	scanErr    error
	ccfeOnPut  bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putInputs = append(f.putInputs, in)
	if f.ccfeOnPut {
		return nil, &dtypes.ConditionalCheckFailedException{}
	}
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	pk, _ := in.Key["pk"].(*dtypes.AttributeValueMemberS)
	if pk == nil {
		return &dynamodb.GetItemOutput{}, nil
	}
	if item, ok := f.getItem[pk.Value]; ok {
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return &dynamodb.ScanOutput{Items: f.scanItems}, nil
}

func (f *fakeDDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.delInputs = append(f.delInputs, in)
	if f.delMissing {
		// AWS returns empty Attributes when the key was missing AND
		// ReturnValues=ALL_OLD; our code reads that as "not found".
		return &dynamodb.DeleteItemOutput{}, nil
	}
	return &dynamodb.DeleteItemOutput{
		Attributes: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "x"},
		},
	}, nil
}

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	fake := &fakeDDB{getItem: map[string]map[string]dtypes.AttributeValue{}}
	ddb.SetClient(fake)
	SetNowFunc(func() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) })
	idSeq, etagSeq := 0, 0
	SetIDFunc(func(int) string { idSeq++; return "id-" + intStr(idSeq) })
	SetEtagFunc(func(int) string { etagSeq++; return "etag-" + intStr(etagSeq) })
	t.Cleanup(func() {
		ddb.SetClient(nil)
		SetNowFunc(func() time.Time { return time.Now().UTC() })
		SetIDFunc(randomHex)
		SetEtagFunc(randomHex)
	})
	return fake
}

func intStr(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{digits[i%10]}, out...)
		i /= 10
	}
	return string(out)
}

func validProfile() Profile {
	return Profile{
		Vertical: "accountants",
		Location: "Manchester, UK",
		Weights: Weights{
			WebsiteAge: 0.2, AuditScore: 0.3, BusinessSize: 0.2,
			ContactConfidence: 0.2, VerticalFit: 0.1,
		},
		Enabled: true,
	}
}

// --- Validate ------------------------------------------------------------

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Profile)
		wantErr string
	}{
		{"valid", func(_ *Profile) {}, ""},
		{"missing vertical", func(p *Profile) { p.Vertical = "" }, "vertical is required"},
		{"missing location", func(p *Profile) { p.Location = "" }, "location is required"},
		{"weights too high", func(p *Profile) { p.Weights.WebsiteAge = 1.0 }, "must sum to 1.0"},
		{"weights too low", func(p *Profile) { p.Weights.WebsiteAge = 0 }, "must sum to 1.0"},
		{"negative weight", func(p *Profile) {
			p.Weights.WebsiteAge = 0.5
			p.Weights.AuditScore = -0.2
			p.Weights.BusinessSize = 0.2
			p.Weights.ContactConfidence = 0.2
			p.Weights.VerticalFit = 0.3
		}, "must be non-negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := validProfile()
			c.mutate(&p)
			err := p.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
			} else {
				if err == nil || !contains(err.Error(), c.wantErr) {
					t.Fatalf("expected error containing %q, got %v", c.wantErr, err)
				}
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// --- Create --------------------------------------------------------------

func TestCreate_AssignsIDEtagTimestamps_ZerosStats(t *testing.T) {
	fake := reset(t)
	in := validProfile()
	in.Stats = Stats{Discovered7d: 999} // attempt to lie about stats

	out, err := Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ID != "id-1" {
		t.Errorf("id = %q, want id-1", out.ID)
	}
	if out.Etag != "etag-1" {
		t.Errorf("etag = %q, want etag-1", out.Etag)
	}
	if out.CreatedAt != "2026-05-11T12:00:00Z" {
		t.Errorf("createdAt = %q", out.CreatedAt)
	}
	if out.Stats.Discovered7d != 0 {
		t.Errorf("Create did not zero stats; got %+v", out.Stats)
	}
	if len(fake.putInputs) != 1 {
		t.Fatalf("expected 1 PutItem, got %d", len(fake.putInputs))
	}
	put := fake.putInputs[0]
	if pk, _ := put.Item["pk"].(*dtypes.AttributeValueMemberS); pk == nil || pk.Value != "TARGET#id-1" {
		t.Errorf("pk wrong: %+v", put.Item["pk"])
	}
	if t1, _ := put.Item["type"].(*dtypes.AttributeValueMemberS); t1 == nil || t1.Value != "TargetingProfile" {
		t.Errorf("type missing/wrong: %+v", put.Item["type"])
	}
	// Enabled profile: gsi1 keys present.
	if gsi1pk, _ := put.Item["gsi1pk"].(*dtypes.AttributeValueMemberS); gsi1pk == nil || gsi1pk.Value != "TARGET#ENABLED" {
		t.Errorf("gsi1pk missing/wrong for enabled profile: %+v", put.Item["gsi1pk"])
	}
}

func TestCreate_DisabledProfileSkipsGSI1Keys(t *testing.T) {
	fake := reset(t)
	in := validProfile()
	in.Enabled = false

	if _, err := Create(context.Background(), in); err != nil {
		t.Fatalf("Create: %v", err)
	}
	put := fake.putInputs[0]
	if _, ok := put.Item["gsi1pk"]; ok {
		t.Errorf("disabled profile should not have gsi1pk; got %+v", put.Item["gsi1pk"])
	}
}

func TestCreate_RejectsInvalid(t *testing.T) {
	reset(t)
	bad := validProfile()
	bad.Vertical = ""

	_, err := Create(context.Background(), bad)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

// --- Get -----------------------------------------------------------------

func TestGet_ReturnsNotFoundForMissingRow(t *testing.T) {
	reset(t)
	_, err := Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGet_RoundTripsAfterCreate(t *testing.T) {
	fake := reset(t)
	created, err := Create(context.Background(), validProfile())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Seed the fake's GetItem map with the persisted row.
	fake.getItem["TARGET#"+created.ID] = fake.putInputs[0].Item

	got, err := Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.Vertical != created.Vertical {
		t.Errorf("got %+v, want %+v", got, created)
	}
}

// --- Update --------------------------------------------------------------

func TestUpdate_RequiresEtag(t *testing.T) {
	fake := reset(t)
	created, _ := Create(context.Background(), validProfile())
	fake.getItem["TARGET#"+created.ID] = fake.putInputs[0].Item

	_, err := Update(context.Background(), created.ID, validProfile(), "")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for missing etag, got %v", err)
	}
}

func TestUpdate_PreservesIDCreatedAtAndStats(t *testing.T) {
	fake := reset(t)
	created, _ := Create(context.Background(), validProfile())
	// Pretend analytics has updated stats since create.
	persisted := fake.putInputs[0].Item
	persisted["stats"] = &dtypes.AttributeValueMemberM{
		Value: map[string]dtypes.AttributeValue{
			"discovered7d": &dtypes.AttributeValueMemberN{Value: "42"},
			"qualified7d":  &dtypes.AttributeValueMemberN{Value: "7"},
			"approved7d":   &dtypes.AttributeValueMemberN{Value: "2"},
		},
	}
	fake.getItem["TARGET#"+created.ID] = persisted

	mod := validProfile()
	mod.ID = "different-id-caller-tried-to-spoof"
	mod.CreatedAt = "2020-01-01T00:00:00Z"
	mod.Stats = Stats{Discovered7d: 999, Qualified7d: 999, Approved7d: 999}
	mod.Vertical = "lawyers" // legitimate edit
	mod.Etag = created.Etag

	out, err := Update(context.Background(), created.ID, mod, created.Etag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ID != created.ID {
		t.Errorf("ID was overwritten: got %q want %q", out.ID, created.ID)
	}
	if out.CreatedAt != created.CreatedAt {
		t.Errorf("CreatedAt was overwritten: got %q want %q", out.CreatedAt, created.CreatedAt)
	}
	if out.Stats.Discovered7d != 42 {
		t.Errorf("Stats were overwritten by API caller: got %+v want from-existing 42", out.Stats)
	}
	if out.Vertical != "lawyers" {
		t.Errorf("legitimate field edit didn't take: got %q", out.Vertical)
	}
	if out.Etag == created.Etag {
		t.Errorf("etag was not rotated on update: %q", out.Etag)
	}
}

func TestUpdate_EtagMismatch(t *testing.T) {
	fake := reset(t)
	created, _ := Create(context.Background(), validProfile())
	fake.getItem["TARGET#"+created.ID] = fake.putInputs[0].Item
	// Force the PUT to fail with the conditional-check exception.
	fake.ccfeOnPut = true

	_, err := Update(context.Background(), created.ID, validProfile(), "stale-etag")
	if !errors.Is(err, ErrEtagMismatch) {
		t.Fatalf("expected ErrEtagMismatch, got %v", err)
	}
}

// --- Delete --------------------------------------------------------------

func TestDelete_ReturnsNotFoundForMissingRow(t *testing.T) {
	fake := reset(t)
	fake.delMissing = true
	err := Delete(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_HappyPath(t *testing.T) {
	fake := reset(t)
	if err := Delete(context.Background(), "id-x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fake.delInputs) != 1 {
		t.Fatalf("expected 1 DeleteItem call, got %d", len(fake.delInputs))
	}
	pk, _ := fake.delInputs[0].Key["pk"].(*dtypes.AttributeValueMemberS)
	if pk == nil || pk.Value != "TARGET#id-x" {
		t.Errorf("delete pk wrong: %+v", fake.delInputs[0].Key["pk"])
	}
}

// --- List ----------------------------------------------------------------

func TestList_ReturnsAllProfiles(t *testing.T) {
	fake := reset(t)

	rows := []map[string]dtypes.AttributeValue{}
	for _, vert := range []string{"accountants", "lawyers", "dentists"} {
		p := validProfile()
		p.Vertical = vert
		item, _ := attributevalue.MarshalMap(p)
		item["pk"] = &dtypes.AttributeValueMemberS{Value: "TARGET#" + vert}
		item["sk"] = &dtypes.AttributeValueMemberS{Value: "PROFILE"}
		item["type"] = &dtypes.AttributeValueMemberS{Value: "TargetingProfile"}
		rows = append(rows, item)
	}
	fake.scanItems = rows

	out, err := List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d profiles, want 3", len(out))
	}
}

func TestList_EmptyTable(t *testing.T) {
	reset(t)
	out, err := List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if out == nil {
		t.Fatal("List returned nil; want empty slice")
	}
	if len(out) != 0 {
		t.Fatalf("got %d, want 0", len(out))
	}
}
