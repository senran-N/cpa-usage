package pricing

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

//go:embed prices.json
var embeddedPricesJSON []byte

// ModelPricing represents pricing details for an AI model.
type ModelPricing struct {
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	LiteLLMProvider                     string  `json:"litellm_provider"`
	Mode                                string  `json:"mode"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
}

// CostBreakdown holds the calculated cost results in USD.
type CostBreakdown struct {
	InputCost         float64 `json:"input_cost"`
	OutputCost        float64 `json:"output_cost"`
	CacheReadCost     float64 `json:"cache_read_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost"`
	TotalCost         float64 `json:"total_cost"`
	MatchedModel      string  `json:"matched_model"`
}

// Engine manages model pricing calculations.
type Engine struct {
	mu          sync.RWMutex
	pricingData map[string]*ModelPricing
}

var (
	defaultEngine     *Engine
	defaultEngineOnce sync.Once
)

// Default returns the singleton pricing engine.
func Default() *Engine {
	defaultEngineOnce.Do(func() {
		defaultEngine = NewEngine()
	})
	return defaultEngine
}

// NewEngine creates and initializes a pricing engine with embedded data.
func NewEngine() *Engine {
	e := &Engine{
		pricingData: make(map[string]*ModelPricing),
	}
	e.loadEmbeddedPrices()
	return e
}

func (e *Engine) loadEmbeddedPrices() {
	if len(embeddedPricesJSON) == 0 {
		return
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(embeddedPricesJSON, &rawMap); err != nil {
		return
	}

	for k, v := range rawMap {
		var p ModelPricing
		if err := json.Unmarshal(v, &p); err == nil {
			kLower := strings.ToLower(strings.TrimSpace(k))
			e.pricingData[kLower] = &p
		}
	}
}

// Fallback pricing for common standard models
var staticFallbacks = map[string]*ModelPricing{
	// OpenAI
	"gpt-4o": {
		InputCostPerToken:       2.5e-06,
		OutputCostPerToken:      1.0e-05,
		CacheReadInputTokenCost: 1.25e-06,
		SupportsPromptCaching:   true,
	},
	"gpt-4o-mini": {
		InputCostPerToken:       1.5e-07,
		OutputCostPerToken:      6.0e-07,
		CacheReadInputTokenCost: 7.5e-08,
		SupportsPromptCaching:   true,
	},
	"gpt-4-turbo": {
		InputCostPerToken:       1.0e-05,
		OutputCostPerToken:      3.0e-05,
		CacheReadInputTokenCost: 5.0e-06,
		SupportsPromptCaching:   true,
	},
	"gpt-4": {
		InputCostPerToken:  3.0e-05,
		OutputCostPerToken: 6.0e-05,
	},
	"gpt-3.5-turbo": {
		InputCostPerToken:  5.0e-07,
		OutputCostPerToken: 1.5e-06,
	},
	"o1": {
		InputCostPerToken:       1.5e-05,
		OutputCostPerToken:      6.0e-05,
		CacheReadInputTokenCost: 7.5e-06,
		SupportsPromptCaching:   true,
	},
	"o1-mini": {
		InputCostPerToken:       1.1e-06,
		OutputCostPerToken:      4.4e-06,
		CacheReadInputTokenCost: 5.5e-07,
		SupportsPromptCaching:   true,
	},
	"o3-mini": {
		InputCostPerToken:       1.1e-06,
		OutputCostPerToken:      4.4e-06,
		CacheReadInputTokenCost: 5.5e-07,
		SupportsPromptCaching:   true,
	},
	// Anthropic Claude
	"claude-3-7-sonnet": {
		InputCostPerToken:           3.0e-06,
		OutputCostPerToken:          1.5e-05,
		CacheCreationInputTokenCost: 3.75e-06,
		CacheReadInputTokenCost:     3.0e-07,
		SupportsPromptCaching:       true,
	},
	"claude-3-5-sonnet": {
		InputCostPerToken:           3.0e-06,
		OutputCostPerToken:          1.5e-05,
		CacheCreationInputTokenCost: 3.75e-06,
		CacheReadInputTokenCost:     3.0e-07,
		SupportsPromptCaching:       true,
	},
	"claude-3-5-haiku": {
		InputCostPerToken:           8.0e-07,
		OutputCostPerToken:          4.0e-06,
		CacheCreationInputTokenCost: 1.0e-06,
		CacheReadInputTokenCost:     8.0e-08,
		SupportsPromptCaching:       true,
	},
	"claude-3-opus": {
		InputCostPerToken:           1.5e-05,
		OutputCostPerToken:          7.5e-05,
		CacheCreationInputTokenCost: 1.875e-05,
		CacheReadInputTokenCost:     1.5e-06,
		SupportsPromptCaching:       true,
	},
	// Google Gemini
	"gemini-2.5-pro": {
		InputCostPerToken:       1.25e-06,
		OutputCostPerToken:      5.0e-06,
		CacheReadInputTokenCost: 3.125e-07,
		SupportsPromptCaching:   true,
	},
	"gemini-2.0-flash": {
		InputCostPerToken:       1.0e-07,
		OutputCostPerToken:      4.0e-07,
		CacheReadInputTokenCost: 2.5e-08,
		SupportsPromptCaching:   true,
	},
	"gemini-1.5-pro": {
		InputCostPerToken:       1.25e-06,
		OutputCostPerToken:      5.0e-06,
		CacheReadInputTokenCost: 3.125e-07,
		SupportsPromptCaching:   true,
	},
	"gemini-1.5-flash": {
		InputCostPerToken:       7.5e-08,
		OutputCostPerToken:      3.0e-07,
		CacheReadInputTokenCost: 1.875e-08,
		SupportsPromptCaching:   true,
	},
	// DeepSeek
	"deepseek-chat": {
		InputCostPerToken:       1.4e-07,
		OutputCostPerToken:      2.8e-07,
		CacheReadInputTokenCost: 1.4e-08,
		SupportsPromptCaching:   true,
	},
	"deepseek-reasoner": {
		InputCostPerToken:       2.7e-07,
		OutputCostPerToken:      1.1e-06,
		CacheReadInputTokenCost: 7.0e-08,
		SupportsPromptCaching:   true,
	},
}

// DeepSeek official rates
const (
	deepseekFlashOffPeakInputPrice  = 1.4e-07 // $0.14 per MTok
	deepseekFlashOffPeakOutputPrice = 2.8e-07 // $0.28 per MTok
	deepseekFlashOffPeakCacheRead   = 1.4e-08 // $0.014 per MTok

	deepseekProOffPeakInputPrice  = 2.7e-07 // $0.27 per MTok
	deepseekProOffPeakOutputPrice = 1.1e-06 // $1.10 per MTok
	deepseekProOffPeakCacheRead   = 7.0e-08 // $0.07 per MTok
)

func isDeepSeekModel(model string) bool {
	return strings.HasPrefix(model, "deepseek-")
}

func isDeepSeekProModel(model string) bool {
	return strings.Contains(model, "pro") || strings.Contains(model, "reasoner") || strings.Contains(model, "r1")
}

func deepseekPeakMultiplierAt(t time.Time) float64 {
	if t.IsZero() {
		t = time.Now()
	}
	beijing := t.In(time.FixedZone("Asia/Shanghai", 8*3600))
	if beijing.Weekday() == time.Saturday || beijing.Weekday() == time.Sunday {
		return 1.0
	}
	h := t.UTC().Hour()
	if (h >= 1 && h < 4) || (h >= 6 && h < 10) {
		return 2.0
	}
	return 1.0
}

// GetModelPricing returns pricing for the requested model with fuzzy matching.
func (e *Engine) GetModelPricing(modelName string, at time.Time) (*ModelPricing, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	if modelLower == "" {
		return nil, ""
	}

	// 1. Check DeepSeek special pricing policy
	if isDeepSeekModel(modelLower) {
		peakMult := deepseekPeakMultiplierAt(at)
		if isDeepSeekProModel(modelLower) {
			return &ModelPricing{
				InputCostPerToken:       deepseekProOffPeakInputPrice * peakMult,
				OutputCostPerToken:      deepseekProOffPeakOutputPrice * peakMult,
				CacheReadInputTokenCost: deepseekProOffPeakCacheRead * peakMult,
				SupportsPromptCaching:   true,
			}, "deepseek-reasoner"
		}
		return &ModelPricing{
			InputCostPerToken:       deepseekFlashOffPeakInputPrice * peakMult,
			OutputCostPerToken:      deepseekFlashOffPeakOutputPrice * peakMult,
			CacheReadInputTokenCost: deepseekFlashOffPeakCacheRead * peakMult,
			SupportsPromptCaching:   true,
		}, "deepseek-chat"
	}

	// 2. Exact match in catalog
	if p, ok := e.pricingData[modelLower]; ok {
		return p, modelLower
	}

	// 3. Strip prefixes like "models/" or "publishers/google/models/"
	cleaned := modelLower
	cleaned = strings.TrimPrefix(cleaned, "models/")
	if idx := strings.LastIndex(cleaned, "/models/"); idx != -1 {
		cleaned = cleaned[idx+len("/models/"):]
	}
	if p, ok := e.pricingData[cleaned]; ok {
		return p, cleaned
	}

	// 4. Exact match in static fallbacks
	if p, ok := staticFallbacks[modelLower]; ok {
		return p, modelLower
	}
	if p, ok := staticFallbacks[cleaned]; ok {
		return p, cleaned
	}

	// 5. Claude Family Matching
	if strings.Contains(cleaned, "claude") {
		switch {
		case strings.Contains(cleaned, "3-7") || strings.Contains(cleaned, "3.7"):
			return staticFallbacks["claude-3-7-sonnet"], "claude-3-7-sonnet"
		case strings.Contains(cleaned, "opus"):
			return staticFallbacks["claude-3-opus"], "claude-3-opus"
		case strings.Contains(cleaned, "haiku"):
			return staticFallbacks["claude-3-5-haiku"], "claude-3-5-haiku"
		case strings.Contains(cleaned, "sonnet"):
			return staticFallbacks["claude-3-5-sonnet"], "claude-3-5-sonnet"
		default:
			return staticFallbacks["claude-3-5-sonnet"], "claude-3-5-sonnet"
		}
	}

	// 6. OpenAI Model Family Matching
	if strings.HasPrefix(cleaned, "gpt-") || strings.HasPrefix(cleaned, "o1") || strings.HasPrefix(cleaned, "o3") {
		switch {
		case strings.Contains(cleaned, "o3-mini"):
			return staticFallbacks["o3-mini"], "o3-mini"
		case strings.Contains(cleaned, "o1-mini"):
			return staticFallbacks["o1-mini"], "o1-mini"
		case strings.HasPrefix(cleaned, "o1"):
			return staticFallbacks["o1"], "o1"
		case strings.Contains(cleaned, "gpt-4o-mini"):
			return staticFallbacks["gpt-4o-mini"], "gpt-4o-mini"
		case strings.Contains(cleaned, "gpt-4o"):
			return staticFallbacks["gpt-4o"], "gpt-4o"
		case strings.Contains(cleaned, "gpt-4-turbo"):
			return staticFallbacks["gpt-4-turbo"], "gpt-4-turbo"
		case strings.Contains(cleaned, "gpt-4"):
			return staticFallbacks["gpt-4"], "gpt-4"
		case strings.Contains(cleaned, "gpt-3.5"):
			return staticFallbacks["gpt-3.5-turbo"], "gpt-3.5-turbo"
		}
	}

	// 7. Gemini Family Matching
	if strings.Contains(cleaned, "gemini") {
		switch {
		case strings.Contains(cleaned, "2.5-pro") || strings.Contains(cleaned, "2-5-pro"):
			return staticFallbacks["gemini-2.5-pro"], "gemini-2.5-pro"
		case strings.Contains(cleaned, "2.0") || strings.Contains(cleaned, "2-0"):
			return staticFallbacks["gemini-2.0-flash"], "gemini-2.0-flash"
		case strings.Contains(cleaned, "1.5-pro") || strings.Contains(cleaned, "1-5-pro"):
			return staticFallbacks["gemini-1.5-pro"], "gemini-1.5-pro"
		case strings.Contains(cleaned, "1.5-flash") || strings.Contains(cleaned, "1-5-flash"):
			return staticFallbacks["gemini-1.5-flash"], "gemini-1.5-flash"
		case strings.Contains(cleaned, "flash"):
			return staticFallbacks["gemini-2.0-flash"], "gemini-2.0-flash"
		case strings.Contains(cleaned, "pro"):
			return staticFallbacks["gemini-2.5-pro"], "gemini-2.5-pro"
		}
	}

	// 8. Catalog substring search fallback.
	// Map iteration order is randomized, so pick the longest matching catalog key
	// instead of the first one encountered. Without this the same model name can
	// resolve to different prices on different runs.
	bestKey := ""
	var bestPricing *ModelPricing
	for k, p := range e.pricingData {
		if !strings.Contains(cleaned, k) && !strings.Contains(k, cleaned) {
			continue
		}
		if len(k) > len(bestKey) || (len(k) == len(bestKey) && k < bestKey) {
			bestKey = k
			bestPricing = p
		}
	}
	if bestPricing != nil {
		return bestPricing, bestKey
	}

	return nil, ""
}

// CalculateCost computes detailed costs for a request based on token usage.
func (e *Engine) CalculateCost(
	modelName string,
	requestedAt time.Time,
	inputTokens, outputTokens, reasoningTokens, cacheReadTokens, cacheCreationTokens int64,
) CostBreakdown {
	pricing, matched := e.GetModelPricing(modelName, requestedAt)
	if pricing == nil {
		return CostBreakdown{
			MatchedModel: modelName,
		}
	}

	// In standard LLM accounting (OpenAI/Anthropic), prompt tokens (inputTokens) includes cache hits.
	// We separate actual fresh input tokens from cache read and cache creation.
	actualInput := inputTokens - cacheReadTokens - cacheCreationTokens
	if actualInput < 0 {
		actualInput = 0
	}

	inputCost := float64(actualInput) * pricing.InputCostPerToken
	outputCost := float64(outputTokens) * pricing.OutputCostPerToken

	cacheReadPrice := pricing.CacheReadInputTokenCost
	if cacheReadPrice <= 0 && pricing.SupportsPromptCaching {
		// Default to 50% of input price if prompt caching is supported but price is unspecified
		cacheReadPrice = pricing.InputCostPerToken * 0.5
	}
	cacheReadCost := float64(cacheReadTokens) * cacheReadPrice

	cacheCreationPrice := pricing.CacheCreationInputTokenCost
	if cacheCreationPrice <= 0 && pricing.SupportsPromptCaching {
		// Anthropic default cache write is 1.25x input price
		if strings.Contains(strings.ToLower(matched), "claude") {
			cacheCreationPrice = pricing.InputCostPerToken * 1.25
		}
	}
	cacheCreationCost := float64(cacheCreationTokens) * cacheCreationPrice

	totalCost := inputCost + outputCost + cacheReadCost + cacheCreationCost

	return CostBreakdown{
		InputCost:         inputCost,
		OutputCost:        outputCost,
		CacheReadCost:     cacheReadCost,
		CacheCreationCost: cacheCreationCost,
		TotalCost:         totalCost,
		MatchedModel:      matched,
	}
}
