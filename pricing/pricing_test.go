package pricing

import (
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
