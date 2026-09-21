package pricing

import (
	"math"
	"testing"
	"time"
)

func TestPricingCalculation(t *testing.T) {
	engine := Default()

	// 1. Test GPT-4o calculation
	cost := engine.CalculateCost("gpt-4o", time.Now(), 1000, 500, 0, 0, 0)
	if cost.TotalCost <= 0 {
		t.Fatalf("expected positive cost for gpt-4o, got %f", cost.TotalCost)
	}
	expectedInput := float64(1000) * 2.5e-06
	expectedOutput := float64(500) * 1.0e-05
	expectedTotal := expectedInput + expectedOutput
	if cost.TotalCost != expectedTotal {
		t.Errorf("expected %f, got %f", expectedTotal, cost.TotalCost)
	}

	// 2. Test Claude 3.5 Sonnet with Prompt Caching
	claudeCost := engine.CalculateCost("claude-3-5-sonnet-20241022", time.Now(), 10000, 1000, 0, 4000, 2000)
	if claudeCost.TotalCost <= 0 {
		t.Fatalf("expected positive cost for claude sonnet, got %f", claudeCost.TotalCost)
	}
	if claudeCost.CacheReadCost <= 0 {
		t.Errorf("expected positive cache read cost, got %f", claudeCost.CacheReadCost)
	}
	if claudeCost.CacheCreationCost <= 0 {
		t.Errorf("expected positive cache creation cost, got %f", claudeCost.CacheCreationCost)
	}

	// 3. Test DeepSeek Chat off-peak
	// Sunday 08:00 UTC -> off-peak (weekend)
	sunday := time.Date(2025, 3, 9, 8, 0, 0, 0, time.UTC)
	dsCost := engine.CalculateCost("deepseek-chat", sunday, 10000, 2000, 0, 5000, 0)
	if dsCost.TotalCost <= 0 {
		t.Fatalf("expected positive cost for deepseek-chat, got %f", dsCost.TotalCost)
	}

	// 4. Test Prefix Stripping: models/gemini-2.5-pro
	geminiCost := engine.CalculateCost("models/gemini-2.5-pro", time.Now(), 2000, 1000, 0, 0, 0)
	if geminiCost.TotalCost <= 0 {
		t.Fatalf("expected positive cost for models/gemini-2.5-pro, got %f", geminiCost.TotalCost)
	}
}

// The host forwards each upstream's native token convention, so the same
// counters mean different things per provider. These cases mirror what
// CLIProxyAPI's parsers actually emit.
func TestResolveConvention(t *testing.T) {
	cases := []struct {
		name          string
		usage         Usage
		wantCacheIn   bool
		wantReasonOut bool
	}{
		{
			// parseOpenAIStyleUsageNode: total = input + output,
			// cache ⊂ input, reasoning ⊂ output.
			name: "openai style",
			usage: Usage{
				InputTokens: 10000, OutputTokens: 2000, ReasoningTokens: 800,
				CacheReadTokens: 6000, TotalTokens: 12000,
			},
			wantCacheIn: true, wantReasonOut: true,
		},
		{
			// parseClaudeUsageNode: total = input + output + cacheRead + cacheWrite,
			// cache is independent from input.
			name: "claude style",
			usage: Usage{
				InputTokens: 2000, OutputTokens: 1500, ReasoningTokens: 500,
				CacheReadTokens: 50000, CacheCreationTokens: 5000, TotalTokens: 58500,
			},
			wantCacheIn: false, wantReasonOut: true,
		},
		{
			// parseGeminiFamilyUsageDetail: total = input + output + reasoning,
			// thinking tokens sit outside candidatesTokenCount.
			name: "gemini style",
			usage: Usage{
				InputTokens: 8000, OutputTokens: 1200, ReasoningTokens: 3000,
				CacheReadTokens: 4000, TotalTokens: 12200,
			},
			wantCacheIn: true, wantReasonOut: false,
		},
		{
			// Cache larger than input can only mean the two are disjoint,
			// regardless of whether a total was reported.
			name: "no total, cache exceeds input",
			usage: Usage{
				InputTokens: 2000, OutputTokens: 1000,
				CacheReadTokens: 40000,
			},
			wantCacheIn: false, wantReasonOut: true,
		},
		{
			// Likewise for reasoning against the output bucket.
			name: "no total, reasoning exceeds output",
			usage: Usage{
				InputTokens: 2000, OutputTokens: 100, ReasoningTokens: 900,
			},
			wantCacheIn: true, wantReasonOut: false,
		},
		{
			// With no cache and no reasoning the conventions coincide.
			name:  "plain request",
			usage: Usage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},

			wantCacheIn: true, wantReasonOut: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveConvention(tc.usage)
			if got.InputIncludesCache != tc.wantCacheIn {
				t.Errorf("InputIncludesCache = %v, want %v", got.InputIncludesCache, tc.wantCacheIn)
			}
			if got.OutputIncludesReasoning != tc.wantReasonOut {
				t.Errorf("OutputIncludesReasoning = %v, want %v", got.OutputIncludesReasoning, tc.wantReasonOut)
			}
		})
	}
}

// Claude reports input already net of cache. Subtracting again drove the
// uncached input to zero and silently dropped the input charge.
func TestClaudeInputNotDoubleSubtracted(t *testing.T) {
	engine := NewEngine()
	u := Usage{
		InputTokens: 2000, OutputTokens: 1000,
		CacheReadTokens: 50000, CacheCreationTokens: 5000,
		TotalTokens: 58000,
	}
	cost := engine.CalculateUsageCost("claude-3-5-sonnet-20241022", time.Now(), u)

	if cost.InputCost <= 0 {
		t.Fatalf("InputCost = %v, want the 2000 uncached tokens to be charged", cost.InputCost)
	}
	p, _ := engine.GetModelPricing("claude-3-5-sonnet-20241022", time.Now())
	want := 2000 * p.InputCostPerToken
	if math.Abs(cost.InputCost-want) > 1e-12 {
		t.Errorf("InputCost = %v, want %v (2000 tokens at the uncached rate)", cost.InputCost, want)
	}
	if cost.CacheReadCost <= 0 || cost.CacheCreationCost <= 0 {
		t.Errorf("cache costs must still be charged, got read=%v creation=%v",
			cost.CacheReadCost, cost.CacheCreationCost)
	}
}

// Gemini reports thinking tokens outside candidatesTokenCount, so they were
// never billed at all.
func TestGeminiReasoningIsBilled(t *testing.T) {
	engine := NewEngine()
	withReasoning := Usage{
		InputTokens: 8000, OutputTokens: 1200, ReasoningTokens: 3000,
		TotalTokens: 12200,
	}
	withoutReasoning := Usage{
		InputTokens: 8000, OutputTokens: 1200,
		TotalTokens: 9200,
	}

	a := engine.CalculateUsageCost("gemini-2.5-pro", time.Now(), withReasoning)
	b := engine.CalculateUsageCost("gemini-2.5-pro", time.Now(), withoutReasoning)

	if a.OutputCost <= b.OutputCost {
		t.Fatalf("reasoning tokens were not billed: with=%v without=%v", a.OutputCost, b.OutputCost)
	}
	p, _ := engine.GetModelPricing("gemini-2.5-pro", time.Now())
	want := float64(1200+3000) * p.OutputCostPerToken
	if math.Abs(a.OutputCost-want) > 1e-12 {
		t.Errorf("OutputCost = %v, want %v (output plus thinking tokens)", a.OutputCost, want)
	}
}

// OpenAI-style records must keep behaving exactly as before.
func TestOpenAIStyleUnchanged(t *testing.T) {
	engine := NewEngine()
	u := Usage{
		InputTokens: 10000, OutputTokens: 2000, ReasoningTokens: 800,
		CacheReadTokens: 6000, TotalTokens: 12000,
	}
	cost := engine.CalculateUsageCost("gpt-4o", time.Now(), u)

	p, _ := engine.GetModelPricing("gpt-4o", time.Now())
	wantInput := float64(10000-6000) * p.InputCostPerToken
	wantOutput := float64(2000) * p.OutputCostPerToken
	if math.Abs(cost.InputCost-wantInput) > 1e-12 {
		t.Errorf("InputCost = %v, want %v", cost.InputCost, wantInput)
	}
	if math.Abs(cost.OutputCost-wantOutput) > 1e-12 {
		t.Errorf("OutputCost = %v, want %v (reasoning already inside output)", cost.OutputCost, wantOutput)
	}
}
