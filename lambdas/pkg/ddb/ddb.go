// Package ddb provides DynamoDB helpers shared across ai-website-agency Lambdas.
// Convention: every item has primary key (pk, sk) of type string. Use
// PK("user", id) to build well-formed keys; this prevents stringly-typed bugs.
package ddb

import (
	"context"
	"fmt"
	"os"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// API is the subset of *dynamodb.Client that shared packages depend on. The
// concrete *dynamodb.Client satisfies it structurally. Widen as later
// iterations need more operations.
type API interface {
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

var (
	clientMu sync.RWMutex
	client   API
)

// Client returns the package-level DynamoDB client, lazily constructed using
// the default AWS credential chain on first call. Override with SetClient in
// tests to inject a fake.
func Client(ctx context.Context) (API, error) {
	clientMu.RLock()
	c := client
	clientMu.RUnlock()
	if c != nil {
		return c, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ddb: loading AWS config: %w", err)
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if client == nil {
		client = dynamodb.NewFromConfig(cfg)
	}
	return client, nil
}

// SetClient overrides the cached client. Pass nil to clear (forces the next
// Client() call to rebuild from default config). Intended for tests.
func SetClient(c API) {
	clientMu.Lock()
	client = c
	clientMu.Unlock()
}

// TableName returns the items-table name from the ITEMS_TABLE env var, which
// Terraform sets on every Lambda invocation environment.
func TableName() string {
	return os.Getenv("ITEMS_TABLE")
}

// PK constructs a DynamoDB partition key with a type prefix.
//
//	PK("user", "abc123") -> "user#abc123"
func PK(kind, id string) string {
	return fmt.Sprintf("%s#%s", kind, id)
}

// SK constructs a DynamoDB sort key with a type prefix.
//
//	SK("metadata", "v1") -> "metadata#v1"
func SK(kind, id string) string {
	return fmt.Sprintf("%s#%s", kind, id)
}
