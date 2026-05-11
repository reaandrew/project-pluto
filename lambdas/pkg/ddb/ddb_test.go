package ddb

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type stubAPI struct{}

func (stubAPI) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func (stubAPI) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}

func (stubAPI) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

func (stubAPI) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}

func (stubAPI) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

func TestSetClientOverridesAndClientReturnsIt(t *testing.T) {
	t.Cleanup(func() { SetClient(nil) })

	stub := stubAPI{}
	SetClient(stub)

	got, err := Client(context.Background())
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if got != stub {
		t.Errorf("Client returned different instance after SetClient")
	}
}

func TestClientLazyInitsRealClientWhenNotSet(t *testing.T) {
	t.Cleanup(func() { SetClient(nil) })
	SetClient(nil) // ensure cleared

	got, err := Client(context.Background())
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if got == nil {
		t.Fatal("Client returned nil")
	}
	// no network call — just verify the API contract is satisfied
	var _ API = got
}

func TestPK(t *testing.T) {
	if got, want := PK("user", "abc"), "user#abc"; got != want {
		t.Errorf("PK = %q, want %q", got, want)
	}
}

func TestSK(t *testing.T) {
	if got, want := SK("metadata", "v1"), "metadata#v1"; got != want {
		t.Errorf("SK = %q, want %q", got, want)
	}
}

func TestTableNameReadsEnv(t *testing.T) {
	t.Setenv("ITEMS_TABLE", "items-test")
	if got, want := TableName(), "items-test"; got != want {
		t.Errorf("TableName = %q, want %q", got, want)
	}
}

func TestTableNameEmptyByDefault(t *testing.T) {
	t.Setenv("ITEMS_TABLE", "")
	if got := TableName(); got != "" {
		t.Errorf("TableName = %q, want empty", got)
	}
}
