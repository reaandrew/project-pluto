// Package httpresp provides standardised JSON / error responses for API Gateway v2
// HTTP API integrations. Keeps Lambda handlers terse.
package httpresp

import "github.com/aws/aws-lambda-go/events"

// JSON returns an APIGatewayV2HTTPResponse with the given status and JSON body.
// Caller is responsible for marshalling.
func JSON(status int, body string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Cache-Control": "no-store",
		},
		Body: body,
	}
}

// Error returns a JSON error envelope with the given status and message.
func Error(status int, message string) events.APIGatewayV2HTTPResponse {
	return JSON(status, `{"error":"`+escape(message)+`"}`)
}

// escape minimal JSON string escaping. Anything richer should pass through json.Marshal.
func escape(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, c)
		}
	}
	return string(b)
}
