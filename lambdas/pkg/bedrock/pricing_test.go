package bedrock

import "testing"

func TestComputeUSDForKnownModels(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		input       int
		output      int
		wantInRange [2]float64
	}{
		{"haiku tiny", ModelHaiku45, 1_000, 500, [2]float64{0.001, 0.0035}},
		{"sonnet medium", ModelSonnet46, 5_000, 2_000, [2]float64{0.030, 0.060}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeUSD(tc.model, tc.input, tc.output)
			if got < tc.wantInRange[0] || got > tc.wantInRange[1] {
				t.Errorf("ComputeUSD(%s, %d, %d) = %f, want in [%f,%f]",
					tc.model, tc.input, tc.output, got, tc.wantInRange[0], tc.wantInRange[1])
			}
		})
	}
}

func TestComputeUSDUnknownModelReturnsZero(t *testing.T) {
	if got := ComputeUSD("anthropic.unknown-model", 1000, 1000); got != 0 {
		t.Errorf("unknown model should return 0, got %f", got)
	}
}

func TestSetPricingOverridesAndIsPickedUpByCompute(t *testing.T) {
	const m = "test.model"
	t.Cleanup(func() { delete(pricing, m) })

	SetPricing(m, 1.0/1_000_000, 5.0/1_000_000)
	got := ComputeUSD(m, 1_000_000, 1_000_000)
	if got < 5.99 || got > 6.01 {
		t.Errorf("ComputeUSD with custom pricing = %f, want ~6.0", got)
	}
}
