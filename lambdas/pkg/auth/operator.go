// Package auth provides shared HTTP-API authorisation helpers for
// ai-website-agency Lambdas. Authentication itself is performed by the
// API Gateway V2 JWT authorizer (terraform/cognito.tf): a request that
// reaches the handler has a verified Cognito ID token. These helpers
// inspect the claims to make finer-grained decisions — e.g. operator
// vs end-user scope — that the JWT authorizer can't express alone.
package auth

import (
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// GroupOperator names the Cognito user-pool group that gates the
// admin-shell APIs (terraform/cognito.tf: aws_cognito_user_group.operator).
const GroupOperator = "operator"

// IsOperator reports whether the caller's verified JWT carries the
// "operator" group claim. Returns false (rather than erroring) for
// missing/empty claims so handlers can simply gate with a 403.
//
// API Gateway V2 stringifies the cognito:groups array into the
// authorizer claims map. The exact serialization varies across deploys
// — observed forms include "[operator]", "[group1 group2]",
// `["group1","group2"]`, and a bare "operator" when only one group is
// present. parseGroups normalises all of them.
func IsOperator(req events.APIGatewayV2HTTPRequest) bool {
	for _, g := range Groups(req) {
		if g == GroupOperator {
			return true
		}
	}
	return false
}

// Groups returns the verified caller's Cognito groups, parsed from the
// authorizer JWT-claims context. Returns nil when no authorizer context
// is attached (tests, mis-routed unauthenticated requests).
func Groups(req events.APIGatewayV2HTTPRequest) []string {
	if req.RequestContext.Authorizer == nil || req.RequestContext.Authorizer.JWT == nil {
		return nil
	}
	return parseGroups(req.RequestContext.Authorizer.JWT.Claims["cognito:groups"])
}

// parseGroups normalises the stringified groups claim into a slice of
// plain group names. Whitespace, commas, brackets, and double-quotes
// are stripped — i.e. it accepts every observed serialization form.
func parseGroups(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.Trim(raw, "[]")
	splitter := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '"':
			return true
		}
		return false
	}
	parts := strings.FieldsFunc(raw, splitter)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
