package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultLiteLLMURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	DefaultOpenRouterURL = "https://openrouter.ai/api/v1/models"
	DefaultModelsDevURL  = "https://models.dev/catalog.json"

	maxSyncBodyBytes = 50 * 1024 * 1024 // 50MB max payload
)

// SyncResult summarizes the outcome of a pricing sync operation.
type SyncResult struct {
	Source        string `json:"source"`
	URL           string `json:"url"`
	ImportedCount int    `json:"imported_count"`
	SkippedCount  int    `json:"skipped_count"`
	TotalCatalog  int    `json:"total_catalog"`
	DurationMs    int64  `json:"duration_ms"`
}

// ModelPricingItem is a comprehensive representation of a model's pricing.
type ModelPricingItem struct {
	Model                               string  `json:"model"`
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	PromptPerM                          float64 `json:"prompt_per_m"`
	CompletionPerM                      float64 `json:"completion_per_m"`
	CacheReadPerM                       float64 `json:"cache_read_per_m"`
	CacheCreationPerM                   float64 `json:"cache_creation_per_m"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	IsCustom                            bool    `json:"is_custom"`
	Source                              string  `json:"source,omitempty"`
	UpdatedAt                           int64   `json:"updated_at,omitempty"`
}

func parsePriceFloat(val any) (float64, bool) {
	if val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ParseLiteLLM parses prices formatted as LiteLLM model_prices_and_context_window.json.
func ParseLiteLLM(data []byte) (map[string]*ModelPricing, int, error) {
	var raw map[string]map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, 0, fmt.Errorf("invalid LiteLLM JSON: %w", err)
	}

	prices := make(map[string]*ModelPricing, len(raw))
	skipped := 0
	now := time.Now().Unix()

	for modelID, entry := range raw {
		modelID = strings.ToLower(strings.TrimSpace(modelID))
		if modelID == "" {
			skipped++
			continue
		}

		inputCost, hasInput := parsePriceFloat(entry["input_cost_per_token"])
		outputCost, hasOutput := parsePriceFloat(entry["output_cost_per_token"])
		cacheReadCost, _ := parsePriceFloat(entry["cache_read_input_token_cost"])
		cacheCreateCost, _ := parsePriceFloat(entry["cache_creation_input_token_cost"])
		cacheCreateAbove1hr, _ := parsePriceFloat(entry["cache_creation_input_token_cost_above_1hr"])

		if !hasInput && !hasOutput && cacheReadCost == 0 && cacheCreateCost == 0 {
			skipped++
			continue
		}

		litellmProv, _ := entry["litellm_provider"].(string)
		mode, _ := entry["mode"].(string)
		supportsCache, _ := entry["supports_prompt_caching"].(bool)
		if !supportsCache && (cacheReadCost > 0 || cacheCreateCost > 0) {
			supportsCache = true
		}

		prices[modelID] = &ModelPricing{
			InputCostPerToken:                   inputCost,
			OutputCostPerToken:                  outputCost,
			CacheReadInputTokenCost:             cacheReadCost,
			CacheCreationInputTokenCost:         cacheCreateCost,
			CacheCreationInputTokenCostAbove1hr: cacheCreateAbove1hr,
			LiteLLMProvider:                     litellmProv,
			Mode:                                mode,
			SupportsPromptCaching:               supportsCache,
			Source:                              "litellm",
			UpdatedAt:                           now,
		}
	}

	return prices, skipped, nil
}

// ParseOpenRouter parses prices formatted from OpenRouter /api/v1/models response.
func ParseOpenRouter(data []byte) (map[string]*ModelPricing, int, error) {
	var raw struct {
		Data []struct {
			ID      string         `json:"id"`
			Pricing map[string]any `json:"pricing"`
		} `json:"data"`
	}

	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, 0, fmt.Errorf("invalid OpenRouter JSON: %w", err)
	}

	prices := make(map[string]*ModelPricing, len(raw.Data)*2)
	skipped := 0
	now := time.Now().Unix()

	for _, item := range raw.Data {
		fullID := strings.ToLower(strings.TrimSpace(item.ID))
		if fullID == "" || item.Pricing == nil {
			skipped++
			continue
		}

		promptCost, hasPrompt := parsePriceFloat(item.Pricing["prompt"])
		completionCost, hasCompletion := parsePriceFloat(item.Pricing["completion"])
		cacheReadCost, _ := parsePriceFloat(item.Pricing["input_cache_read"])
		cacheCreateCost, _ := parsePriceFloat(item.Pricing["input_cache_write"])

		if !hasPrompt && !hasCompletion && cacheReadCost == 0 && cacheCreateCost == 0 {
			skipped++
			continue
		}

		supportsCache := cacheReadCost > 0 || cacheCreateCost > 0
		p := &ModelPricing{
			InputCostPerToken:           promptCost,
			OutputCostPerToken:          completionCost,
			CacheReadInputTokenCost:     cacheReadCost,
			CacheCreationInputTokenCost: cacheCreateCost,
			SupportsPromptCaching:       supportsCache,
			Source:                      "openrouter",
			UpdatedAt:                   now,
		}

		prices[fullID] = p

		// If fullID has provider prefix (e.g. openai/gpt-4o), also register gpt-4o if not yet present
		if parts := strings.Split(fullID, "/"); len(parts) == 2 {
			shortID := parts[1]
			if _, exists := prices[shortID]; !exists {
				shortPricing := *p
				prices[shortID] = &shortPricing
			}
		}
	}

	return prices, skipped, nil
}

// ParseModelsDev parses prices formatted from models.dev catalog.json.
func ParseModelsDev(data []byte) (map[string]*ModelPricing, int, error) {
	var root map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, 0, fmt.Errorf("invalid models.dev JSON: %w", err)
	}

	providerMessages := root
	if rawProviders, hasProviders := root["providers"]; hasProviders {
		var catalogProviders map[string]json.RawMessage
		if err := json.Unmarshal(rawProviders, &catalogProviders); err == nil {
			providerMessages = catalogProviders
		}
	}

	prices := make(map[string]*ModelPricing, 1024)
	skipped := 0
	now := time.Now().Unix()

	for providerID, msg := range providerMessages {
		var prov struct {
			Models map[string]struct {
				Cost map[string]any `json:"cost"`
			} `json:"models"`
		}
		if err := json.Unmarshal(msg, &prov); err != nil {
			continue
		}

		for modelID, m := range prov.Models {
			fullID := strings.ToLower(strings.TrimSpace(providerID + "/" + modelID))
			shortID := strings.ToLower(strings.TrimSpace(modelID))
			if fullID == "" || m.Cost == nil {
				skipped++
				continue
			}

			// In models.dev, input/output costs are given in USD per 1,000,000 tokens
			inputM, hasInput := parsePriceFloat(m.Cost["input"])
			outputM, hasOutput := parsePriceFloat(m.Cost["output"])
			cacheReadM, _ := parsePriceFloat(m.Cost["cache_read"])
			cacheWriteM, _ := parsePriceFloat(m.Cost["cache_write"])

			if !hasInput && !hasOutput && cacheReadM == 0 && cacheWriteM == 0 {
				skipped++
				continue
			}

			p := &ModelPricing{
				InputCostPerToken:           inputM / 1_000_000.0,
				OutputCostPerToken:          outputM / 1_000_000.0,
				CacheReadInputTokenCost:     cacheReadM / 1_000_000.0,
				CacheCreationInputTokenCost: cacheWriteM / 1_000_000.0,
				SupportsPromptCaching:       cacheReadM > 0 || cacheWriteM > 0,
				Source:                      "models.dev",
				UpdatedAt:                   now,
			}

			prices[fullID] = p
			if _, exists := prices[shortID]; !exists {
				shortP := *p
				prices[shortID] = &shortP
			}
		}
	}

	if len(prices) == 0 {
		return nil, skipped, errors.New("no usable models found in models.dev catalog")
	}

	return prices, skipped, nil
}

// ParsePricesJSON auto-detects or dispatches to the right parser.
func ParsePricesJSON(data []byte, preferredSource string) (map[string]*ModelPricing, int, string, error) {
	normSource := strings.ToLower(strings.TrimSpace(preferredSource))

	switch normSource {
	case "litellm":
		res, skipped, err := ParseLiteLLM(data)
		return res, skipped, "litellm", err
	case "openrouter":
		res, skipped, err := ParseOpenRouter(data)
		return res, skipped, "openrouter", err
	case "models.dev", "modelsdev":
		res, skipped, err := ParseModelsDev(data)
		return res, skipped, "models.dev", err
	}

	// Auto-detect based on payload content
	strPrefix := string(data)
	if len(strPrefix) > 1024 {
		strPrefix = strPrefix[:1024]
	}

	if strings.Contains(strPrefix, `"data"`) && strings.Contains(strPrefix, `"pricing"`) {
		if res, skipped, err := ParseOpenRouter(data); err == nil && len(res) > 0 {
			return res, skipped, "openrouter", nil
		}
	}

	if strings.Contains(strPrefix, `"providers"`) {
		if res, skipped, err := ParseModelsDev(data); err == nil && len(res) > 0 {
			return res, skipped, "models.dev", nil
		}
	}

	// Default to LiteLLM format
	if res, skipped, err := ParseLiteLLM(data); err == nil && len(res) > 0 {
		return res, skipped, "litellm", nil
	}

	// Fallbacks
	if res, skipped, err := ParseOpenRouter(data); err == nil && len(res) > 0 {
		return res, skipped, "openrouter", nil
	}
	if res, skipped, err := ParseModelsDev(data); err == nil && len(res) > 0 {
		return res, skipped, "models.dev", nil
	}

	return nil, 0, "unknown", errors.New("unable to auto-detect pricing JSON format (tried litellm, openrouter, models.dev)")
}

// validateSyncURL rejects sync targets that are not plain http(s) fetches.
//
// The sync endpoint accepts a caller-supplied URL, so without this the server
// could be pointed at non-HTTP schemes (file://, gopher://, ...) and made to
// read local resources on the caller's behalf.
func validateSyncURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid price sync URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported price sync URL scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return errors.New("price sync URL is missing a host")
	}
	return nil
}

// SyncPrices fetches prices from an online source, parses them, and updates the engine.
func (e *Engine) SyncPrices(ctx context.Context, source string, customURL string) (*SyncResult, map[string]*ModelPricing, error) {
	start := time.Now()
	normSource := strings.ToLower(strings.TrimSpace(source))

	targetURL := strings.TrimSpace(customURL)
	if targetURL == "" {
		switch normSource {
		case "openrouter":
			targetURL = DefaultOpenRouterURL
		case "models.dev", "modelsdev":
			targetURL = DefaultModelsDevURL
		default:
			targetURL = DefaultLiteLLMURL
			normSource = "litellm"
		}
	}

	if err := validateSyncURL(targetURL); err != nil {
		return nil, nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sync request: %w", err)
	}
	req.Header.Set("User-Agent", "cpa-usage-sync/1.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("price sync HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("price sync upstream returned HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	lr := io.LimitReader(resp.Body, maxSyncBodyBytes)
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	parsedPrices, skipped, detectedSource, err := ParsePricesJSON(body, normSource)
	if err != nil {
		return nil, nil, err
	}

	e.LoadSyncedPrices(parsedPrices)
	total, _, _ := e.GetCatalogSummary()

	res := &SyncResult{
		Source:        detectedSource,
		URL:           targetURL,
		ImportedCount: len(parsedPrices),
		SkippedCount:  skipped,
		TotalCatalog:  total,
		DurationMs:    time.Since(start).Milliseconds(),
	}

	return res, parsedPrices, nil
}
