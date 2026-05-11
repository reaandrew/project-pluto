package csv

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/discovery"
)

// --- parseCSV --------------------------------------------------------

func TestParseCSV_HappyPath(t *testing.T) {
	in := strings.NewReader(`name,domain,location,vertical
Acme Accountants,acmeaccountants.co.uk,Manchester,accountants
Beta Tax,betatax.co.uk,Leeds,accountants
`)
	out, err := parseCSV(in, 0)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].Name != "Acme Accountants" || out[0].Domain != "acmeaccountants.co.uk" {
		t.Errorf("row 0 = %+v", out[0])
	}
	if out[0].Source != "csv" {
		t.Errorf("source = %q, want csv", out[0].Source)
	}
	if out[0].Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", out[0].Confidence)
	}
	if out[1].Location != "Leeds" {
		t.Errorf("row 1 location = %q", out[1].Location)
	}
}

func TestParseCSV_CapStopsEarly(t *testing.T) {
	in := strings.NewReader(`name,domain
a,a.com
b,b.com
c,c.com
`)
	out, err := parseCSV(in, 2)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d, want 2", len(out))
	}
}

func TestParseCSV_SkipsRowsWithoutName(t *testing.T) {
	in := strings.NewReader(`name,domain
,no-name.com
Real Co,real.com
   ,whitespace.com
`)
	out, err := parseCSV(in, 0)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Real Co" {
		t.Fatalf("expected only Real Co, got %+v", out)
	}
}

func TestParseCSV_HeaderIsCaseInsensitive(t *testing.T) {
	in := strings.NewReader(`NAME,Domain,LOCATION
x,x.com,Leeds
`)
	out, err := parseCSV(in, 0)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if out[0].Name != "x" || out[0].Domain != "x.com" || out[0].Location != "Leeds" {
		t.Errorf("case-insensitive header lookup failed: %+v", out[0])
	}
}

func TestParseCSV_ExtraColumnsAreIgnored(t *testing.T) {
	in := strings.NewReader(`name,domain,note,owner
x,x.com,seed list,operator
`)
	out, err := parseCSV(in, 0)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(out) != 1 || out[0].Domain != "x.com" {
		t.Errorf("extra columns broke parse: %+v", out)
	}
}

func TestParseCSV_MissingNameHeaderRejects(t *testing.T) {
	in := strings.NewReader(`domain,vertical
x.com,accountants
`)
	_, err := parseCSV(in, 0)
	if err == nil {
		t.Fatal("expected error for missing name header, got nil")
	}
	if !strings.Contains(err.Error(), "missing `name`") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestParseCSV_EmptyFileReturnsEmptySlice(t *testing.T) {
	out, err := parseCSV(strings.NewReader(""), 0)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if out == nil {
		t.Fatal("got nil, want empty slice")
	}
	if len(out) != 0 {
		t.Errorf("got %d, want 0", len(out))
	}
}

// --- Provider.Find with injected fake S3 -----------------------------

type fakeS3 struct {
	body io.ReadCloser
	err  error
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &s3.GetObjectOutput{Body: f.body}, nil
}

func TestProvider_Find_ReturnsParsedRows(t *testing.T) {
	body := io.NopCloser(strings.NewReader("name,domain\nAcme,acme.com\n"))
	p := &Provider{
		Bucket: "ai-website-agency-pipeline-blobs",
		Key:    "discovery/inbox/list1.csv",
		Client: &fakeS3{body: body},
	}
	out, err := p.Find(context.Background(), discovery.FindRequest{}, 0)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Acme" {
		t.Fatalf("got %+v", out)
	}
}

func TestProvider_Find_PropagatesGetObjectError(t *testing.T) {
	p := &Provider{
		Bucket: "b",
		Key:    "k",
		Client: &fakeS3{err: errors.New("access denied")},
	}
	_, err := p.Find(context.Background(), discovery.FindRequest{}, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestProvider_Find_RejectsMissingBucketOrKey(t *testing.T) {
	p := &Provider{Bucket: "", Key: "k", Client: &fakeS3{}}
	if _, err := p.Find(context.Background(), discovery.FindRequest{}, 0); err == nil {
		t.Error("expected error for missing bucket")
	}
	p = &Provider{Bucket: "b", Key: "", Client: &fakeS3{}}
	if _, err := p.Find(context.Background(), discovery.FindRequest{}, 0); err == nil {
		t.Error("expected error for missing key")
	}
}

func TestProvider_ID_IsCsv(t *testing.T) {
	p := &Provider{}
	if p.ID() != "csv" {
		t.Errorf("ID() = %q, want csv", p.ID())
	}
	if p.CostPerCallUSD() != 0 {
		t.Errorf("CostPerCallUSD() = %v, want 0", p.CostPerCallUSD())
	}
}
