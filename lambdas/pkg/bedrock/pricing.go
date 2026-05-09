package bedrock

// Model IDs we use. Bedrock-side IDs match these strings; keeping them as
// constants prevents typos from leaking into prompts.
const (
	ModelHaiku45  = "anthropic.claude-haiku-4-5"
	ModelSonnet46 = "anthropic.claude-sonnet-4-6"
)

// pricing maps a Bedrock model ID to per-token costs in USD. Update when
// Anthropic / Bedrock change list price (the cost spec at
// .ralph/specs/05-capacity-and-cost.md targets these as of 2026-Q1).
//
// Per-token, NOT per-million-tokens. Multiply by raw token counts.
type modelPricing struct {
	InputUSDPerToken  float64
	OutputUSDPerToken float64
}

var pricing = map[string]modelPricing{
	ModelHaiku45: {
		InputUSDPerToken:  0.80 / 1_000_000,
		OutputUSDPerToken: 4.00 / 1_000_000,
	},
	ModelSonnet46: {
		InputUSDPerToken:  3.00 / 1_000_000,
		OutputUSDPerToken: 15.00 / 1_000_000,
	},
}

// SetPricing overrides the pricing table for a model. Intended for tests and
// for runtime updates when list price changes; callers can register pricing
// for new models without recompiling pkg/bedrock.
func SetPricing(modelID string, inputUSDPerToken, outputUSDPerToken float64) {
	pricing[modelID] = modelPricing{
		InputUSDPerToken:  inputUSDPerToken,
		OutputUSDPerToken: outputUSDPerToken,
	}
}

// ComputeUSD returns the dollar cost of a single Bedrock invocation given
// the input + output token counts. Returns 0 (and no error) for unknown
// model IDs — the caller still gets a successful invoke; we just can't
// price it. (Operators see this in CloudWatch as a $0 cost-record entry,
// which is louder than a missing entry.)
func ComputeUSD(modelID string, inputTokens, outputTokens int) float64 {
	p, ok := pricing[modelID]
	if !ok {
		return 0
	}
	return float64(inputTokens)*p.InputUSDPerToken + float64(outputTokens)*p.OutputUSDPerToken
}
