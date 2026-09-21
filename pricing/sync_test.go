package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseLiteLLM(t *testing.T) {
	raw := `{
		"gpt-4o-custom": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.00000125,
			"litellm_provider": "openai"
		},
		"empty-model": {}
	}`

	prices, skipped, err := ParseLiteLLM([]byte(raw))
	if err != nil {
		t.Fatalf("ParseLiteLLM failed: %v", err)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	p, ok := prices["gpt-4o-custom"]
	if !ok {
		t.Fatalf("expected gpt-4o-custom to be parsed")
	}
	if p.InputCostPerToken != 0.0000025 {
		t.Errorf("expected input 0.0000025, got %v", p.InputCostPerToken)
	}
	if p.OutputCostPerToken != 0.00001 {
		t.Errorf("expected output 0.00001, got %v", p.OutputCostPerToken)
	}
	if !p.SupportsPromptCaching {
		t.Errorf("expected supports_prompt_caching to be true because cache_read > 0")
	}
}

func TestParseOpenRouter(t *testing.T) {
	raw := `{
		"data": [
			{
				"id": "anthropic/claude-3.5-sonnet",
				"pricing": {
					"prompt": "0.000003",
					"completion": "0.000015",
					"input_cache_read": "0.0000003",
					"input_cache_write": "0.00000375"
				}
			},
			{
				"id": "empty-pricing-model",
				"pricing": {}
			}
		]
	}`

	prices, skipped, err := ParseOpenRouter([]byte(raw))
	if err != nil {
		t.Fatalf("ParseOpenRouter failed: %v", err)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}

	pFull, ok := prices["anthropic/claude-3.5-sonnet"]
	if !ok {
		t.Fatalf("expected full ID anthropic/claude-3.5-sonnet to be present")
	}
	if pFull.InputCostPerToken != 0.000003 {
		t.Errorf("expected 0.000003, got %v", pFull.InputCostPerToken)
	}

	// Verify short alias was also registered
	pShort, ok := prices["claude-3.5-sonnet"]
	if !ok {
		t.Fatalf("expected short alias claude-3.5-sonnet to be present")
	}
	if pShort.OutputCostPerToken != 0.000015 {
		t.Errorf("expected 0.000015, got %v", pShort.OutputCostPerToken)
	}
}

func TestParseModelsDev(t *testing.T) {
	raw := `{
		"providers": {
			"openai": {
				"models": {
					"gpt-4.1": {
						"cost": {
							"input": 2.0,
							"output": 8.0,
							"cache_read": 1.0,
							"cache_write": 2.5
						}
					}
				}
			}
		}
	}`

	prices, skipped, err := ParseModelsDev([]byte(raw))
	if err != nil {
		t.Fatalf("ParseModelsDev failed: %v", err)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}

	p, ok := prices["gpt-4.1"]
	if !ok {
		t.Fatalf("expected gpt-4.1 to be present")
	}
	expectedInput := 2.0 / 1_000_000.0
	if p.InputCostPerToken != expectedInput {
		t.Errorf("expected input %v, got %v", expectedInput, p.InputCostPerToken)
	}
}

func TestCustomOverridePrecedence(t *testing.T) {
	engine := NewEngine()

	// Default gpt-4o price in static/embedded
	pOrig, _ := engine.GetModelPricing("gpt-4o", time.Now())
	if pOrig == nil {
		t.Fatalf("expected gpt-4o default pricing to exist")
	}

	// Set custom override
	customInput := 1.11e-6
	engine.SetCustomOverride("gpt-4o", &ModelPricing{
		InputCostPerToken:  customInput,
		OutputCostPerToken: 4.44e-6,
	})

	pOverridden, matched := engine.GetModelPricing("gpt-4o", time.Now())
	if pOverridden == nil || matched != "gpt-4o" {
		t.Fatalf("expected matched gpt-4o, got %s", matched)
	}
	if pOverridden.InputCostPerToken != customInput {
		t.Errorf("expected overridden input %v, got %v", customInput, pOverridden.InputCostPerToken)
	}
	if !pOverridden.IsCustom {
		t.Errorf("expected IsCustom to be true")
	}

	// Delete custom override
	deleted := engine.DeleteCustomOverride("gpt-4o")
	if !deleted {
		t.Errorf("expected DeleteCustomOverride to return true")
	}

	pRestored, _ := engine.GetModelPricing("gpt-4o", time.Now())
	if pRestored.InputCostPerToken == customInput {
		t.Errorf("expected restored price to not be custom input")
	}
}

func TestSyncPricesHTTP(t *testing.T) {
	mockPayload := `{
		"mock-model-synced": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.00002
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockPayload))
	}))
	defer server.Close()

	engine := NewEngine()
	res, parsed, err := engine.SyncPrices(context.Background(), "litellm", server.URL)
	if err != nil {
		t.Fatalf("SyncPrices failed: %v", err)
	}

	if res.ImportedCount != 1 {
		t.Errorf("expected 1 imported, got %d", res.ImportedCount)
	}
	if len(parsed) != 1 {
		t.Errorf("expected 1 parsed price, got %d", len(parsed))
	}

	p, matched := engine.GetModelPricing("mock-model-synced", time.Now())
	if p == nil || matched != "mock-model-synced" {
		t.Fatalf("expected mock-model-synced to be retrievable from engine")
	}
	if p.InputCostPerToken != 0.000005 {
		t.Errorf("expected 0.000005, got %v", p.InputCostPerToken)
	}
}

func TestSearchModelsAndSummary(t *testing.T) {
	engine := NewEngine()
	engine.SetCustomOverride("my-special-llm", &ModelPricing{
		InputCostPerToken:  0.000001,
		OutputCostPerToken: 0.000002,
	})

	total, custom, synced := engine.GetCatalogSummary()
	if custom != 1 {
		t.Errorf("expected 1 custom, got %d", custom)
	}
	if total < 10 {
		t.Errorf("expected total > 10, got %d", total)
	}
	_ = synced

	results := engine.SearchModels("my-special", 10, false)
	if len(results) == 0 {
		t.Fatalf("expected to find my-special-llm")
	}
	if results[0].Model != "my-special-llm" || !results[0].IsCustom {
		t.Errorf("unexpected search result: %+v", results[0])
	}
	if results[0].PromptPerM != 1.0 {
		t.Errorf("expected PromptPerM 1.0, got %v", results[0].PromptPerM)
	}
}
