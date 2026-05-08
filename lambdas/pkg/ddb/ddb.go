// Package ddb provides DynamoDB helpers shared across ai-website-agency Lambdas.
// Convention: every item has primary key (pk, sk) of type string. Use
// PK("user", id) to build well-formed keys; this prevents stringly-typed bugs.
package ddb

import "fmt"

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
