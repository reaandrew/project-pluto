package auth

import (
	"reflect"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestParseGroups(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"single bare", "operator", []string{"operator"}},
		{"single bracketed", "[operator]", []string{"operator"}},
		{"go slice form", "[operator reviewer]", []string{"operator", "reviewer"}},
		{"json array form", `["operator","reviewer"]`, []string{"operator", "reviewer"}},
		{"comma separated", "operator,reviewer", []string{"operator", "reviewer"}},
		{"trailing comma + bracket", `["operator",]`, []string{"operator"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseGroups(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseGroups(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsOperator(t *testing.T) {
	t.Run("nil authorizer", func(t *testing.T) {
		req := events.APIGatewayV2HTTPRequest{}
		if IsOperator(req) {
			t.Fatal("IsOperator returned true for unauthenticated request")
		}
	})

	t.Run("authorizer without operator group", func(t *testing.T) {
		req := withGroups("[reviewer]")
		if IsOperator(req) {
			t.Fatal("IsOperator returned true for non-operator")
		}
	})

	t.Run("authorizer with operator group", func(t *testing.T) {
		req := withGroups("[operator reviewer]")
		if !IsOperator(req) {
			t.Fatal("IsOperator returned false for operator")
		}
	})

	t.Run("operator stand-alone", func(t *testing.T) {
		req := withGroups("operator")
		if !IsOperator(req) {
			t.Fatal("IsOperator returned false for bare-string operator")
		}
	})

	t.Run("operator-prefix is not operator", func(t *testing.T) {
		req := withGroups("[operator-readonly]")
		if IsOperator(req) {
			t.Fatal("IsOperator returned true for similarly-named group")
		}
	})
}

func withGroups(raw string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			Authorizer: &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
				JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
					Claims: map[string]string{"cognito:groups": raw},
				},
			},
		},
	}
}
