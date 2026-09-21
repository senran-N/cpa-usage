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
	LiteLLMProvider                     string  `json:"litellm_provider,omitempty"`
	Mode                                string  `json:"mode,omitempty"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	Source                              string  `json:"source,omitempty"`
	UpdatedAt                           int64   `json:"updated_at,omitempty"`
	IsCustom                            bool    `json:"is_custom,omitempty"`
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
	mu              sync.RWMutex
	pricingData     map[string]*ModelPricing
	syncedData      map[string]*ModelPricing
	customOverrides map[string]*ModelPricing
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
		pricingData:     make(map[string]*ModelPricing),
		syncedData:      make(map[string]*ModelPricing),
		customOverrides: make(map[string]*ModelPricing),
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

	cleaned := modelLower
	cleaned = strings.TrimPrefix(cleaned, "models/")
	if idx := strings.LastIndex(cleaned, "/models/"); idx != -1 {
		cleaned = cleaned[idx+len("/models/"):]
	}

	// 0. User custom overrides take highest priority
	if p, ok := e.customOverrides[modelLower]; ok {
		return p, modelLower
	}
	if p, ok := e.customOverrides[cleaned]; ok {
		return p, cleaned
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
				Source:                  "deepseek-official",
			}, "deepseek-reasoner"
		}
		return &ModelPricing{
			InputCostPerToken:       deepseekFlashOffPeakInputPrice * peakMult,
			OutputCostPerToken:      deepseekFlashOffPeakOutputPrice * peakMult,
			CacheReadInputTokenCost: deepseekFlashOffPeakCacheRead * peakMult,
			SupportsPromptCaching:   true,
			Source:                  "deepseek-official",
		}, "deepseek-chat"
	}

	// 2. Synced pricing from upstream online sources
	if p, ok := e.syncedData[modelLower]; ok {
		return p, modelLower
	}
	if p, ok := e.syncedData[cleaned]; ok {
		return p, cleaned
	}

	// 3. Exact match in catalog
	if p, ok := e.pricingData[modelLower]; ok {
		return p, modelLower
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
	for k, p := range e.syncedData {
		if !strings.Contains(cleaned, k) && !strings.Contains(k, cleaned) {
			continue
		}
		if len(k) > len(bestKey) || (len(k) == len(bestKey) && k < bestKey) {
			bestKey = k
			bestPricing = p
		}
	}
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

// SetCustomOverride stores a custom model price override.
func (e *Engine) SetCustomOverride(model string, p *ModelPricing) {
	if p == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	k := strings.ToLower(strings.TrimSpace(model))
	clone := *p
	clone.IsCustom = true
	if clone.UpdatedAt == 0 {
		clone.UpdatedAt = time.Now().Unix()
	}
	if clone.Source == "" {
		clone.Source = "manual"
	}
	e.customOverrides[k] = &clone
}

// DeleteCustomOverride removes a custom model price override.
func (e *Engine) DeleteCustomOverride(model string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := strings.ToLower(strings.TrimSpace(model))
	if _, ok := e.customOverrides[k]; ok {
		delete(e.customOverrides, k)
		return true
	}
	return false
}

// GetCustomOverrides returns a copy of all current custom overrides.
func (e *Engine) GetCustomOverrides() map[string]*ModelPricing {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make(map[string]*ModelPricing, len(e.customOverrides))
	for k, v := range e.customOverrides {
		clone := *v
		res[k] = &clone
	}
	return res
}

// LoadCustomOverrides loads multiple custom overrides.
func (e *Engine) LoadCustomOverrides(overrides map[string]*ModelPricing) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range overrides {
		key := strings.ToLower(strings.TrimSpace(k))
		clone := *v
		clone.IsCustom = true
		if clone.Source == "" {
			clone.Source = "manual"
		}
		e.customOverrides[key] = &clone
	}
}

// LoadSyncedPrices loads synced prices into the engine.
func (e *Engine) LoadSyncedPrices(prices map[string]*ModelPricing) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range prices {
		key := strings.ToLower(strings.TrimSpace(k))
		clone := *v
		e.syncedData[key] = &clone
	}
}

// SearchModels searches for models matching query across custom, synced, and embedded catalogs.
func (e *Engine) SearchModels(query string, limit int, onlyCustom bool) []ModelPricingItem {
	e.mu.RLock()
	defer e.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 50
	}

	var results []ModelPricingItem
	seen := make(map[string]bool)

	checkAndAppend := func(model string, p *ModelPricing, isCustom bool) bool {
		if seen[model] {
			return false
		}
		if query != "" && !strings.Contains(model, query) {
			return false
		}
		seen[model] = true
		results = append(results, ModelPricingItem{
			Model:                               model,
			InputCostPerToken:                   p.InputCostPerToken,
			OutputCostPerToken:                  p.OutputCostPerToken,
			CacheCreationInputTokenCost:         p.CacheCreationInputTokenCost,
			CacheCreationInputTokenCostAbove1hr: p.CacheCreationInputTokenCostAbove1hr,
			CacheReadInputTokenCost:             p.CacheReadInputTokenCost,
			PromptPerM:                          p.InputCostPerToken * 1_000_000,
			CompletionPerM:                      p.OutputCostPerToken * 1_000_000,
			CacheReadPerM:                       p.CacheReadInputTokenCost * 1_000_000,
			CacheCreationPerM:                   p.CacheCreationInputTokenCost * 1_000_000,
			SupportsPromptCaching:               p.SupportsPromptCaching,
			IsCustom:                            isCustom,
			Source:                              p.Source,
			UpdatedAt:                           p.UpdatedAt,
		})
		return true
	}

	// 1. Custom overrides first
	for k, p := range e.customOverrides {
		if len(results) >= limit {
			return results
		}
		checkAndAppend(k, p, true)
	}

	if onlyCustom {
		return results
	}

	// 2. Synced models second
	for k, p := range e.syncedData {
		if len(results) >= limit {
			return results
		}
		checkAndAppend(k, p, false)
	}

	// 3. Embedded pricing third
	for k, p := range e.pricingData {
		if len(results) >= limit {
			return results
		}
		checkAndAppend(k, p, false)
	}

	return results
}

// GetCatalogSummary returns count statistics of models in the engine.
func (e *Engine) GetCatalogSummary() (total int, custom int, synced int) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	custom = len(e.customOverrides)
	synced = len(e.syncedData)
	distinct := make(map[string]bool)
	for k := range e.customOverrides {
		distinct[k] = true
	}
	for k := range e.syncedData {
		distinct[k] = true
	}
	for k := range e.pricingData {
		distinct[k] = true
	}
	total = len(distinct)
	return total, custom, synced
}

// Usage carries the token counters a single request reported.
//
// The host forwards whatever convention the upstream protocol uses, so the
// same field can mean different things depending on the provider. See
// resolveConvention.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	// TotalTokens is the upstream total. It is what lets the two conventions be
	// told apart; zero means the detection falls back to structural checks.
	TotalTokens int64
}

// convention records how a provider's counters nest, so the cost math can
// avoid both double counting and dropping tokens on the floor.
type convention struct {
	// InputIncludesCache reports whether CacheReadTokens and CacheCreationTokens
	// are already part of InputTokens, in which case they must be subtracted to
	// get the tokens billed at the uncached input rate.
	InputIncludesCache bool
	// OutputIncludesReasoning reports whether ReasoningTokens are already part of
	// OutputTokens. When they are not, they have to be added or the thinking
	// tokens are never billed.
	OutputIncludesReasoning bool
}

// resolveConvention infers how the counters nest.
//
// CLIProxyAPI normalizes this internally into Detail.TokenBreakdown, but only
// the flat counters reach a plugin, so it has to be recovered here. The three
// shapes the host produces are:
//
//	OpenAI style   total = input + output                          (cache ⊂ input, reasoning ⊂ output)
//	Claude style   total = input + output + cacheRead + cacheWrite (cache ⊥ input, reasoning ⊂ output)
//	Gemini style   total = input + output + reasoning              (cache ⊂ input, reasoning ⊥ output)
//
// Matching the reported total against each shape identifies the provider
// without relying on provider names, which are free-form for OpenAI-compatible
// endpoints. Where a counter is zero the shapes coincide and the choice does
// not affect the result.
func resolveConvention(u Usage) convention {
	c := convention{InputIncludesCache: true, OutputIncludesReasoning: true}

	cacheTotal := u.CacheReadTokens + u.CacheCreationTokens

	// Cache cannot be a subset of input if it exceeds it. This holds regardless
	// of whether a total was reported.
	if cacheTotal > u.InputTokens {
		c.InputIncludesCache = false
	} else if u.TotalTokens > 0 && cacheTotal > 0 {
		subset := u.InputTokens + u.OutputTokens
		independent := subset + cacheTotal
		if u.TotalTokens == independent && u.TotalTokens != subset {
			c.InputIncludesCache = false
		}
	}

	// Same reasoning for thinking tokens against the output bucket.
	if u.ReasoningTokens > u.OutputTokens {
		c.OutputIncludesReasoning = false
	} else if u.TotalTokens > 0 && u.ReasoningTokens > 0 {
		subset := u.InputTokens + u.OutputTokens
		separate := subset + u.ReasoningTokens
		if u.TotalTokens == separate && u.TotalTokens != subset {
			c.OutputIncludesReasoning = false
		}
	}

	return c
}

// CalculateCost computes detailed costs for a request based on token usage.
//
// Deprecated: prefer CalculateUsageCost, which takes the reported total and can
// therefore tell the provider conventions apart.
func (e *Engine) CalculateCost(
	modelName string,
	requestedAt time.Time,
	inputTokens, outputTokens, reasoningTokens, cacheReadTokens, cacheCreationTokens int64,
) CostBreakdown {
	return e.CalculateUsageCost(modelName, requestedAt, Usage{
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		ReasoningTokens:     reasoningTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	})
}

// CalculateUsageCost computes detailed costs for a request based on token usage.
func (e *Engine) CalculateUsageCost(modelName string, requestedAt time.Time, u Usage) CostBreakdown {
	pricing, matched := e.GetModelPricing(modelName, requestedAt)
	if pricing == nil {
		return CostBreakdown{
			MatchedModel: modelName,
		}
	}

	conv := resolveConvention(u)

	// Tokens billed at the uncached input rate.
	uncachedInput := u.InputTokens
	if conv.InputIncludesCache {
		uncachedInput -= u.CacheReadTokens + u.CacheCreationTokens
	}
	if uncachedInput < 0 {
		uncachedInput = 0
	}

	// Tokens billed at the output rate. Thinking tokens are charged as output by
	// every provider here; they are only listed apart in some protocols.
	billableOutput := u.OutputTokens
	if !conv.OutputIncludesReasoning {
		billableOutput += u.ReasoningTokens
	}
	if billableOutput < 0 {
		billableOutput = 0
	}

	inputCost := float64(uncachedInput) * pricing.InputCostPerToken
	outputCost := float64(billableOutput) * pricing.OutputCostPerToken

	cacheReadPrice := pricing.CacheReadInputTokenCost
	if cacheReadPrice <= 0 && pricing.SupportsPromptCaching {
		// Default to 50% of input price if prompt caching is supported but price is unspecified
		cacheReadPrice = pricing.InputCostPerToken * 0.5
	}
	cacheReadCost := float64(u.CacheReadTokens) * cacheReadPrice

	cacheCreationPrice := pricing.CacheCreationInputTokenCost
	if cacheCreationPrice <= 0 && pricing.SupportsPromptCaching {
		// Anthropic default cache write is 1.25x input price
		if strings.Contains(strings.ToLower(matched), "claude") {
			cacheCreationPrice = pricing.InputCostPerToken * 1.25
		}
	}
	cacheCreationCost := float64(u.CacheCreationTokens) * cacheCreationPrice

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
