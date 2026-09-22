package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-usage/pricing"
	"cpa-usage/sanitize"
	"cpa-usage/storage"
)

// Handler handles Usage Management API requests.
type Handler struct {
	store   *storage.Storage
	pricing *pricing.Engine
}

// NewHandler creates an API handler with the provided storage and pricing engine.
func NewHandler(store *storage.Storage, pricingEngine *pricing.Engine) *Handler {
	if pricingEngine == nil {
		pricingEngine = pricing.Default()
	}
	return &Handler{
		store:   store,
		pricing: pricingEngine,
	}
}

// Response represents a standard HTTP response to return to CPA.
type Response struct {
	StatusCode int
	Headers    map[string][]string
	Body       []byte
}

// JSONResponse serializes data to JSON and returns a 200 OK Response.
func JSONResponse(status int, data any) Response {
	raw, err := json.Marshal(data)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "json_marshal_error", err.Error())
	}
	return Response{
		StatusCode: status,
		Headers: map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		Body: raw,
	}
}

// ErrorResponse creates an error response payload.
func ErrorResponse(status int, code, message string) Response {
	payload := map[string]any{
		"error":   code,
		"message": message,
	}
	raw, _ := json.Marshal(payload)
	return Response{
		StatusCode: status,
		Headers: map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		Body: raw,
	}
}

// HTMLResponse creates an HTML response.
func HTMLResponse(status int, html []byte) Response {
	return Response{
		StatusCode: status,
		Headers: map[string][]string{
			"Content-Type": {"text/html; charset=utf-8"},
		},
		Body: html,
	}
}

// Handle processes an incoming API request by matching its subpath.
func (h *Handler) Handle(method, path string, query url.Values, body []byte) Response {
	if h.store == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "database_unavailable", "database is not initialized")
	}

	// Usage is ingested asynchronously to keep the host request path fast. A
	// read barrier makes completed requests immediately visible to the dashboard
	// instead of making users wait for the periodic batch flush.
	if err := h.store.Flush(); err != nil {
		return ErrorResponse(http.StatusServiceUnavailable, "database_unavailable", err.Error())
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	cleanPath := strings.TrimRight(strings.TrimSpace(path), "/")

	// Subpath routing (e.g. /summary, /timeseries, /models, /keys, /auths, /records, /filter-options, /cleanup)
	switch {
	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/summary"):
		return h.handleSummary(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/timeseries"):
		return h.handleTimeSeries(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/models"):
		return h.handleModels(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/keys"):
		return h.handleAPIKeys(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/auths"):
		return h.handleAuths(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/records"):
		return h.handleRecords(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/filter-options"):
		return h.handleFilterOptions()

	case method == http.MethodGet && (strings.HasSuffix(cleanPath, "/accounts/health") || strings.HasSuffix(cleanPath, "/health")):
		return h.handleAccountsHealth(query)

	case method == http.MethodGet && (strings.HasSuffix(cleanPath, "/accounts/quota") || strings.HasSuffix(cleanPath, "/quota")):
		return h.handleAccountsQuota(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/diagnostics"):
		return h.handleDiagnostics(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/export"):
		return h.handleExport(query)

	case method == http.MethodPost && strings.HasSuffix(cleanPath, "/import"):
		return h.handleImport(body)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/prices"):
		return h.handleGetPrices(query)

	case (method == http.MethodPost || method == http.MethodGet) && strings.HasSuffix(cleanPath, "/prices/sync"):
		return h.handleSyncPrices(body, query)

	case (method == http.MethodPost || method == http.MethodGet) && strings.HasSuffix(cleanPath, "/prices/override"):
		if method == http.MethodGet && query.Get("action") == "delete" {
			return h.handleDeletePriceOverride(query, body)
		}
		return h.handleSavePriceOverride(body, query)

	case method == http.MethodDelete && strings.HasSuffix(cleanPath, "/prices/override"):
		return h.handleDeletePriceOverride(query, body)

	case (method == http.MethodPost || method == http.MethodDelete || method == http.MethodGet) && strings.HasSuffix(cleanPath, "/cleanup"):
		return h.handleCleanup(query)

	case method == http.MethodGet && strings.HasSuffix(cleanPath, "/ping"):
		return JSONResponse(http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})

	default:
		return ErrorResponse(http.StatusNotFound, "route_not_found", fmt.Sprintf("no route for %s %s", method, path))
	}
}

func parseTimeParam(val string) *time.Time {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}

	// Try unix timestamp
	if sec, err := strconv.ParseInt(val, 10, 64); err == nil {
		// Detect milliseconds vs seconds
		if sec > 1e11 {
			t := time.UnixMilli(sec).UTC()
			return &t
		}
		t := time.Unix(sec, 0).UTC()
		return &t
	}

	// Formats to try
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, val); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

func parseQueryFilter(query url.Values) storage.QueryFilter {
	filter := storage.QueryFilter{
		StartTime: parseTimeParam(query.Get("start_time")),
		EndTime:   parseEndTimeParam(query.Get("end_time")),
		APIKey:    strings.TrimSpace(query.Get("api_key")),
		Model:     strings.TrimSpace(query.Get("model")),
		Provider:  strings.TrimSpace(query.Get("provider")),
		AuthID:    strings.TrimSpace(query.Get("auth_id")),
		Search:    strings.TrimSpace(query.Get("search")),
	}
	if failedStr := strings.TrimSpace(query.Get("failed")); failedStr != "" {
		failed := strings.EqualFold(failedStr, "true") || failedStr == "1"
		filter.Failed = &failed
	}
	if mismatchStr := strings.TrimSpace(query.Get("mismatch")); mismatchStr != "" {
		mismatch := strings.EqualFold(mismatchStr, "true") || mismatchStr == "1"
		filter.ModelMismatch = &mismatch
	}
	return filter
}

// parseEndTimeParam treats a date-only end_time as the end of that local UTC
// day. Without this, selecting a date range ending on 2026-03-30 silently
// excluded every record after 00:00:00 on that date.
func parseEndTimeParam(val string) *time.Time {
	trimmed := strings.TrimSpace(val)
	if len(trimmed) == len("2006-01-02") {
		if t, err := time.Parse("2006-01-02", trimmed); err == nil {
			end := t.UTC().Add(24*time.Hour - time.Nanosecond)
			return &end
		}
	}
	return parseTimeParam(trimmed)
}

func (h *Handler) handleSummary(query url.Values) Response {
	filter := parseQueryFilter(query)
	stats, err := h.store.GetSummary(filter)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, stats)
}

func (h *Handler) handleTimeSeries(query url.Values) Response {
	filter := parseQueryFilter(query)
	interval := strings.ToLower(strings.TrimSpace(query.Get("interval")))
	if interval == "" {
		interval = "hour"
	}
	points, err := h.store.GetTimeSeries(filter, interval)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, points)
}

func (h *Handler) handleModels(query url.Values) Response {
	filter := parseQueryFilter(query)
	stats, err := h.store.GetModelStats(filter)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, stats)
}

func (h *Handler) handleAPIKeys(query url.Values) Response {
	filter := parseQueryFilter(query)
	stats, err := h.store.GetAPIKeyStats(filter)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, stats)
}

func (h *Handler) handleAuths(query url.Values) Response {
	filter := parseQueryFilter(query)
	stats, err := h.store.GetAuthStats(filter)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, stats)
}

func (h *Handler) handleRecords(query url.Values) Response {
	filter := storage.RecordQueryFilter{
		QueryFilter: parseQueryFilter(query),
	}

	page, _ := strconv.Atoi(query.Get("page"))
	if page < 1 {
		page = 1
	}
	filter.Page = page

	// Clamp with the same bounds storage enforces so the page_size reported
	// back to the client is the one actually used for the query. Echoing the
	// raw request made clients paginate with a stride that skipped rows.
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	filter.PageSize = storage.ClampPageSize(pageSize)

	records, total, err := h.store.GetRecords(filter)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}

	return JSONResponse(http.StatusOK, map[string]any{
		"page":      filter.Page,
		"page_size": filter.PageSize,
		"total":     total,
		"records":   records,
	})
}

func (h *Handler) handleFilterOptions() Response {
	opts, err := h.store.GetFilterOptions()
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, opts)
}

func (h *Handler) handleCleanup(query url.Values) Response {
	daysStr := strings.TrimSpace(query.Get("days"))
	beforeStr := strings.TrimSpace(query.Get("before"))

	var before time.Time
	if beforeStr != "" {
		if t := parseTimeParam(beforeStr); t != nil {
			before = *t
		}
	}

	if before.IsZero() {
		if daysStr == "0" || strings.EqualFold(daysStr, "all") {
			before = time.Now().Add(time.Hour)
		} else {
			days, _ := strconv.Atoi(daysStr)
			if days <= 0 {
				days = 90
			}
			before = time.Now().AddDate(0, 0, -days)
		}
	}

	deleted, err := h.store.Cleanup(before)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "cleanup_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, map[string]any{
		"message":      "cleanup completed",
		"deleted_rows": deleted,
		"before":       before.UTC().Format(time.RFC3339),
	})
}

// PriceOverrideRequest represents payload for setting custom model pricing.
type PriceOverrideRequest struct {
	Model                               string   `json:"model"`
	InputCostPerToken                   *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken                  *float64 `json:"output_cost_per_token,omitempty"`
	CacheReadCostPerToken               *float64 `json:"cache_read_cost_per_token,omitempty"`
	CacheCreationCostPerToken           *float64 `json:"cache_creation_cost_per_token,omitempty"`
	CacheCreationInputTokenCostAbove1hr *float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	PromptPerM                          *float64 `json:"prompt_per_m,omitempty"`
	CompletionPerM                      *float64 `json:"completion_per_m,omitempty"`
	CacheReadPerM                       *float64 `json:"cache_read_per_m,omitempty"`
	CacheCreationPerM                   *float64 `json:"cache_creation_per_m,omitempty"`
	SupportsPromptCaching               *bool    `json:"supports_prompt_caching,omitempty"`
	Source                              string   `json:"source,omitempty"`
}

// PriceSyncRequest represents payload for triggering online price sync.
type PriceSyncRequest struct {
	Source string `json:"source,omitempty"`
	URL    string `json:"url,omitempty"`
}

func (h *Handler) handleGetPrices(query url.Values) Response {
	if h.pricing == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "pricing_engine_unavailable", "pricing engine is not initialized")
	}

	search := strings.TrimSpace(query.Get("search"))
	onlyCustom := strings.EqualFold(query.Get("only_custom"), "true") || query.Get("only_custom") == "1"
	all := strings.EqualFold(query.Get("all"), "true") || query.Get("all") == "1"

	limit := 50
	if limitStr := strings.TrimSpace(query.Get("limit")); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if all {
		limit = 100000
	}

	models := h.pricing.SearchModels(search, limit, onlyCustom)
	total, custom, synced := h.pricing.GetCatalogSummary()
	overrides := h.pricing.GetCustomOverrides()

	return JSONResponse(http.StatusOK, map[string]any{
		"total_models":     total,
		"custom_count":     custom,
		"synced_count":     synced,
		"models":           models,
		"custom_overrides": overrides,
	})
}

func (h *Handler) handleSyncPrices(body []byte, query url.Values) Response {
	if h.pricing == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "pricing_engine_unavailable", "pricing engine is not initialized")
	}

	var req PriceSyncRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	if req.Source == "" && query != nil {
		req.Source = strings.TrimSpace(query.Get("source"))
	}
	if req.URL == "" && query != nil {
		req.URL = strings.TrimSpace(query.Get("url"))
		if req.URL == "" {
			req.URL = strings.TrimSpace(query.Get("custom_url"))
		}
	}

	result, parsedPrices, err := h.pricing.SyncPrices(context.Background(), req.Source, req.URL)
	if err != nil {
		return ErrorResponse(http.StatusBadGateway, "sync_failed", err.Error())
	}

	// Persist to storage in background or inline
	if h.store != nil && len(parsedPrices) > 0 {
		records := make([]storage.SyncedPriceRecord, 0, len(parsedPrices))
		for model, p := range parsedPrices {
			records = append(records, storage.SyncedPriceRecord{
				Model:                               model,
				InputCostPerToken:                   p.InputCostPerToken,
				OutputCostPerToken:                  p.OutputCostPerToken,
				CacheReadCostPerToken:               p.CacheReadInputTokenCost,
				CacheCreationCostPerToken:           p.CacheCreationInputTokenCost,
				CacheCreationInputTokenCostAbove1hr: p.CacheCreationInputTokenCostAbove1hr,
				SupportsPromptCaching:               p.SupportsPromptCaching,
				Source:                              p.Source,
				UpdatedAt:                           p.UpdatedAt,
			})
		}
		_ = h.store.SaveSyncedPricesBatch(records)
	}

	return JSONResponse(http.StatusOK, map[string]any{
		"status":        "success",
		"result":        result,
		"synced_models": len(parsedPrices),
	})
}

func (h *Handler) handleSavePriceOverride(body []byte, query url.Values) Response {
	if h.pricing == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "pricing_engine_unavailable", "pricing engine is not initialized")
	}

	var req PriceOverrideRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}

	if req.Model == "" && query != nil {
		req.Model = strings.TrimSpace(query.Get("model"))
		if v, err := strconv.ParseFloat(query.Get("input_price"), 64); err == nil {
			req.PromptPerM = &v
		}
		if v, err := strconv.ParseFloat(query.Get("output_price"), 64); err == nil {
			req.CompletionPerM = &v
		}
		if v, err := strconv.ParseFloat(query.Get("cache_read_price"), 64); err == nil {
			req.CacheReadPerM = &v
		}
		if v, err := strconv.ParseFloat(query.Get("cache_creation_price"), 64); err == nil {
			req.CacheCreationPerM = &v
		}
	}

	model := strings.ToLower(strings.TrimSpace(req.Model))
	if model == "" {
		return ErrorResponse(http.StatusBadRequest, "missing_model", "model name is required")
	}

	inputCost := 0.0
	if req.InputCostPerToken != nil {
		inputCost = *req.InputCostPerToken
	} else if req.PromptPerM != nil {
		inputCost = *req.PromptPerM / 1_000_000.0
	}

	outputCost := 0.0
	if req.OutputCostPerToken != nil {
		outputCost = *req.OutputCostPerToken
	} else if req.CompletionPerM != nil {
		outputCost = *req.CompletionPerM / 1_000_000.0
	}

	cacheReadCost := 0.0
	if req.CacheReadCostPerToken != nil {
		cacheReadCost = *req.CacheReadCostPerToken
	} else if req.CacheReadPerM != nil {
		cacheReadCost = *req.CacheReadPerM / 1_000_000.0
	}

	cacheCreationCost := 0.0
	if req.CacheCreationCostPerToken != nil {
		cacheCreationCost = *req.CacheCreationCostPerToken
	} else if req.CacheCreationPerM != nil {
		cacheCreationCost = *req.CacheCreationPerM / 1_000_000.0
	}

	cacheCreateAbove1hr := 0.0
	if req.CacheCreationInputTokenCostAbove1hr != nil {
		cacheCreateAbove1hr = *req.CacheCreationInputTokenCostAbove1hr
	}

	supportsCache := cacheReadCost > 0 || cacheCreationCost > 0
	if req.SupportsPromptCaching != nil {
		supportsCache = *req.SupportsPromptCaching
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual"
	}
	now := time.Now().Unix()

	pricingModel := &pricing.ModelPricing{
		InputCostPerToken:                   inputCost,
		OutputCostPerToken:                  outputCost,
		CacheReadInputTokenCost:             cacheReadCost,
		CacheCreationInputTokenCost:         cacheCreationCost,
		CacheCreationInputTokenCostAbove1hr: cacheCreateAbove1hr,
		SupportsPromptCaching:               supportsCache,
		Source:                              source,
		UpdatedAt:                           now,
		IsCustom:                            true,
	}

	// Update in-memory engine
	h.pricing.SetCustomOverride(model, pricingModel)

	// Persist to storage
	if h.store != nil {
		if err := h.store.SaveCustomPrice(storage.CustomPriceRecord{
			Model:                               model,
			InputCostPerToken:                   inputCost,
			OutputCostPerToken:                  outputCost,
			CacheReadCostPerToken:               cacheReadCost,
			CacheCreationCostPerToken:           cacheCreationCost,
			CacheCreationInputTokenCostAbove1hr: cacheCreateAbove1hr,
			SupportsPromptCaching:               supportsCache,
			Source:                              source,
			UpdatedAt:                           now,
		}); err != nil {
			return ErrorResponse(http.StatusInternalServerError, "db_save_failed", err.Error())
		}
	}

	return JSONResponse(http.StatusOK, map[string]any{
		"status":  "success",
		"model":   model,
		"pricing": pricingModel,
	})
}

func (h *Handler) handleDeletePriceOverride(query url.Values, body []byte) Response {
	if h.pricing == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "pricing_engine_unavailable", "pricing engine is not initialized")
	}

	model := strings.ToLower(strings.TrimSpace(query.Get("model")))
	if model == "" && len(body) > 0 {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err == nil {
			model = strings.ToLower(strings.TrimSpace(req.Model))
		}
	}

	if model == "" {
		return ErrorResponse(http.StatusBadRequest, "missing_model", "model name is required")
	}

	h.pricing.DeleteCustomOverride(model)
	if h.store != nil {
		if err := h.store.DeleteCustomPrice(model); err != nil {
			return ErrorResponse(http.StatusInternalServerError, "db_delete_failed", err.Error())
		}
	}

	return JSONResponse(http.StatusOK, map[string]any{
		"status":  "success",
		"message": fmt.Sprintf("custom override for %s deleted", model),
		"model":   model,
	})
}

func (h *Handler) handleAccountsHealth(query url.Values) Response {
	if h.store == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "storage_unavailable", "storage is not initialized")
	}
	filter := parseQueryFilter(query)
	now := time.Now().UTC()
	resp, err := h.store.GetAccountsHealth(filter, now)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "accounts_health_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, resp)
}

func (h *Handler) handleAccountsQuota(query url.Values) Response {
	if h.store == nil {
		return ErrorResponse(http.StatusServiceUnavailable, "storage_unavailable", "storage is not initialized")
	}
	filter := parseQueryFilter(query)
	now := time.Now().UTC()
	resp, err := h.store.GetAccountsQuota(filter, now)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "accounts_quota_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, resp)
}

func (h *Handler) handleDiagnostics(query url.Values) Response {
	filter := parseQueryFilter(query)
	recentLimit := 50
	if lStr := strings.TrimSpace(query.Get("recent_limit")); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			recentLimit = l
		}
	}

	stats, err := h.store.GetDiagnosticStats(filter, recentLimit)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "diagnostics_failed", err.Error())
	}
	return JSONResponse(http.StatusOK, stats)
}

func (h *Handler) handleExport(query url.Values) Response {
	filter := parseQueryFilter(query)
	format := strings.ToLower(strings.TrimSpace(query.Get("format")))
	if format == "" {
		format = "jsonl"
	}

	// Bound the export so a single request cannot pull an unbounded number of
	// rows into memory while the full CSV/JSON payload is buffered.
	limit := storage.DefaultExportLimit
	if lStr := strings.TrimSpace(query.Get("limit")); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = storage.ClampExportLimit(l)
		}
	}

	records, err := h.store.ExportRecords(filter, limit)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "export_failed", err.Error())
	}

	timeStr := time.Now().UTC().Format("20060102-150405")
	headers := make(http.Header)

	switch format {
	case "csv":
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		headerRow := []string{
			"id", "requested_at", "provider", "model", "auth_id", "api_key",
			"generate", "latency_ms", "failed", "failure_status_code", "fail_summary",
			"header_error_kind", "header_error_code", "header_trace_id",
			"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_creation_tokens", "total_tokens",
			"input_cost", "output_cost", "total_cost",
		}
		_ = w.Write(headerRow)

		for _, r := range records {
			genStr := "true"
			if !r.Generate {
				genStr = "false"
			}
			failStr := "false"
			if r.Failed {
				failStr = "true"
			}
			row := []string{
				strconv.FormatInt(r.ID, 10),
				r.RequestedAt.UTC().Format(time.RFC3339),
				r.Provider,
				r.Model,
				r.AuthID,
				r.APIKey,
				genStr,
				strconv.FormatInt(r.LatencyMs, 10),
				failStr,
				strconv.Itoa(r.FailureStatusCode),
				r.FailSummary,
				r.HeaderErrorKind,
				r.HeaderErrorCode,
				r.HeaderTraceID,
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.ReasoningTokens, 10),
				strconv.FormatInt(r.CacheReadTokens, 10),
				strconv.FormatInt(r.CacheCreationTokens, 10),
				strconv.FormatInt(r.TotalTokens, 10),
				fmt.Sprintf("%.6f", r.InputCost),
				fmt.Sprintf("%.6f", r.OutputCost),
				fmt.Sprintf("%.6f", r.TotalCost),
			}
			_ = w.Write(row)
		}
		w.Flush()

		headers.Set("Content-Type", "text/csv; charset=utf-8")
		headers.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="usage-export-%s.csv"`, timeStr))
		return Response{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       buf.Bytes(),
		}

	case "json":
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return ErrorResponse(http.StatusInternalServerError, "json_marshal_failed", err.Error())
		}
		headers.Set("Content-Type", "application/json; charset=utf-8")
		headers.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="usage-export-%s.json"`, timeStr))
		return Response{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       data,
		}

	default: // "jsonl" or "ndjson"
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, r := range records {
			_ = enc.Encode(r)
		}
		headers.Set("Content-Type", "application/x-ndjson; charset=utf-8")
		headers.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="usage-export-%s.jsonl"`, timeStr))
		return Response{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       buf.Bytes(),
		}
	}
}

type rawImportItem struct {
	Provider            string     `json:"provider"`
	BaseURL             string     `json:"base_url"`
	ExecutorType        string     `json:"executor_type"`
	Model               string     `json:"model"`
	Alias               string     `json:"alias"`
	APIKey              string     `json:"api_key"`
	SessionID           string     `json:"session_id"`
	ParentSessionID     string     `json:"parent_session_id"`
	AuthID              string     `json:"auth_id"`
	AuthIndex           string     `json:"auth_index"`
	AuthType            string     `json:"auth_type"`
	Source              string     `json:"source"`
	ReasoningEffort     string     `json:"reasoning_effort"`
	ServiceTier         string     `json:"service_tier"`
	Generate            *bool      `json:"generate"`
	RequestedAt         *time.Time `json:"requested_at"`
	RequestedAtUnix     *int64     `json:"requested_at_unix"`
	LatencyMs           int64      `json:"latency_ms"`
	TTFTMs              int64      `json:"ttft_ms"`
	Failed              *bool      `json:"failed"`
	FailureStatusCode   int        `json:"failure_status_code"`
	FailureBody         string     `json:"failure_body"`
	FailSummary         string     `json:"fail_summary"`
	HeaderErrorKind     string     `json:"header_error_kind"`
	HeaderErrorCode     string     `json:"header_error_code"`
	HeaderTraceID       string     `json:"header_trace_id"`
	InputTokens         int64      `json:"input_tokens"`
	OutputTokens        int64      `json:"output_tokens"`
	ReasoningTokens     int64      `json:"reasoning_tokens"`
	CachedTokens        int64      `json:"cached_tokens"`
	CacheReadTokens     int64      `json:"cache_read_tokens"`
	CacheCreationTokens int64      `json:"cache_creation_tokens"`
	TotalTokens         int64      `json:"total_tokens"`
	InputCost           float64    `json:"input_cost"`
	OutputCost          float64    `json:"output_cost"`
	CacheReadCost       float64    `json:"cache_read_cost"`
	CacheCreationCost   float64    `json:"cache_creation_cost"`
	TotalCost           float64    `json:"total_cost"`
	MatchedModel        string     `json:"matched_model"`
	ResponseModel       string     `json:"response_model"`
	AltRespModel        string     `json:"responseModel"`
	ModelMismatch       *bool      `json:"model_mismatch"`

	// CPA-Manager-Plus compatibility fields
	EventHash      string  `json:"event_hash"`
	TimestampMs    int64   `json:"timestamp_ms"`
	Timestamp      string  `json:"timestamp"`
	CreatedAtMs    int64   `json:"created_at_ms"`
	Endpoint       string  `json:"endpoint"`
	FailStatusCode int     `json:"fail_status_code"`
	Error          string  `json:"error"`
	Cost           float64 `json:"cost"`
}

func (h *Handler) handleImport(body []byte) Response {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ErrorResponse(http.StatusBadRequest, "empty_payload", "import payload is empty")
	}

	records, format, parseErrors := h.parseImportPayload(trimmed)
	if len(records) == 0 && parseErrors > 0 {
		return ErrorResponse(http.StatusBadRequest, "invalid_payload", fmt.Sprintf("failed to parse any usage records (%d parse errors)", parseErrors))
	}

	imported, skipped, err := h.store.ImportRecords(records)
	if err != nil {
		return ErrorResponse(http.StatusInternalServerError, "import_failed", err.Error())
	}

	return JSONResponse(http.StatusOK, map[string]any{
		"status":       "success",
		"format":       format,
		"total_parsed": len(records),
		"imported":     imported,
		"skipped":      skipped,
		"parse_errors": parseErrors,
	})
}

func (h *Handler) parseImportPayload(data []byte) ([]*storage.Record, string, int) {
	if len(data) == 0 {
		return nil, "", 0
	}

	var rawItems []rawImportItem
	format := "jsonl"
	parseErrors := 0

	if data[0] == '[' {
		format = "json"
		if err := json.Unmarshal(data, &rawItems); err != nil {
			return nil, format, 1
		}
	} else {
		// Line-by-line JSONL
		scanner := bufio.NewScanner(bytes.NewReader(data))
		// Support large lines up to 4MB
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 4*1024*1024)

		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var item rawImportItem
			if err := json.Unmarshal(line, &item); err != nil {
				parseErrors++
				continue
			}
			rawItems = append(rawItems, item)
		}
	}

	records := make([]*storage.Record, 0, len(rawItems))
	for _, item := range rawItems {
		rec := h.convertImportItem(item)
		if rec != nil {
			records = append(records, rec)
		}
	}

	return records, format, parseErrors
}

func (h *Handler) convertImportItem(item rawImportItem) *storage.Record {
	model := strings.TrimSpace(item.Model)
	if model == "" {
		return nil
	}

	provider := strings.TrimSpace(item.Provider)
	if provider == "" {
		if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") {
			provider = "openai"
		} else if strings.HasPrefix(model, "claude-") {
			provider = "anthropic"
		} else if strings.HasPrefix(model, "gemini-") {
			provider = "google"
		} else {
			provider = "openai"
		}
	}

	sessionID := strings.TrimSpace(item.SessionID)
	if sessionID == "" && item.EventHash != "" {
		sessionID = strings.TrimSpace(item.EventHash)
	}

	var reqAt time.Time
	if item.RequestedAt != nil && !item.RequestedAt.IsZero() {
		reqAt = item.RequestedAt.UTC()
	} else if item.TimestampMs > 0 {
		reqAt = time.UnixMilli(item.TimestampMs).UTC()
	} else if item.CreatedAtMs > 0 {
		reqAt = time.UnixMilli(item.CreatedAtMs).UTC()
	} else if item.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, item.Timestamp); err == nil {
			reqAt = t.UTC()
		} else if t, err := time.Parse("2006-01-02 15:04:05", item.Timestamp); err == nil {
			reqAt = t.UTC()
		}
	} else if item.RequestedAtUnix != nil && *item.RequestedAtUnix > 0 {
		reqAt = time.Unix(*item.RequestedAtUnix, 0).UTC()
	}
	if reqAt.IsZero() {
		reqAt = time.Now().UTC()
	}

	statusCode := item.FailureStatusCode
	if statusCode == 0 && item.FailStatusCode > 0 {
		statusCode = item.FailStatusCode
	}

	failed := false
	if item.Failed != nil {
		failed = *item.Failed
	} else if statusCode >= 400 {
		failed = true
	}

	// Imported payloads are untrusted and bypass the live ingest path, so apply
	// the same credential redaction here. Otherwise an import can write secrets
	// back into the database that ingestion would have stripped.
	failureBody := sanitize.SanitizeDiagnosticBody(item.FailureBody)

	failSummary := strings.TrimSpace(item.FailSummary)
	if failSummary == "" && item.Error != "" {
		failSummary = strings.TrimSpace(item.Error)
	}
	if failSummary != "" {
		failSummary = sanitize.SanitizeCredentialText(failSummary)
	} else if failureBody != "" {
		failSummary = sanitize.FailSummaryFromBody(failureBody)
	}

	gen := true
	if item.Generate != nil {
		gen = *item.Generate
	}

	totalTokens := item.TotalTokens
	if totalTokens == 0 {
		totalTokens = item.InputTokens + item.OutputTokens
	}

	inputCost := item.InputCost
	outputCost := item.OutputCost
	totalCost := item.TotalCost
	if totalCost == 0 && item.Cost > 0 {
		totalCost = item.Cost
	}

	// Calculate cost using pricing engine if missing
	if totalCost == 0 && inputCost == 0 && totalTokens > 0 && h.pricing != nil {
		calc := h.pricing.CalculateUsageCost(model, reqAt, pricing.Usage{
			InputTokens:         item.InputTokens,
			OutputTokens:        item.OutputTokens,
			ReasoningTokens:     item.ReasoningTokens,
			CacheReadTokens:     item.CacheReadTokens,
			CacheCreationTokens: item.CacheCreationTokens,
			TotalTokens:         totalTokens,
		})
		inputCost = calc.InputCost
		outputCost = calc.OutputCost
		totalCost = calc.TotalCost
	}

	respModel := strings.TrimSpace(item.ResponseModel)
	if respModel == "" {
		respModel = strings.TrimSpace(item.AltRespModel)
	}
	mismatch := false
	if item.ModelMismatch != nil {
		mismatch = *item.ModelMismatch
	} else if respModel != "" {
		mismatch = storage.DetectModelMismatch(item.Alias, model, respModel)
	}

	return &storage.Record{
		Provider:            provider,
		BaseURL:             item.BaseURL,
		ExecutorType:        item.ExecutorType,
		Model:               model,
		Alias:               item.Alias,
		ResponseModel:       respModel,
		ModelMismatch:       mismatch,
		APIKey:              item.APIKey,
		SessionID:           sessionID,
		ParentSessionID:     item.ParentSessionID,
		AuthID:              item.AuthID,
		AuthIndex:           item.AuthIndex,
		AuthType:            item.AuthType,
		Source:              item.Source,
		ReasoningEffort:     item.ReasoningEffort,
		ServiceTier:         item.ServiceTier,
		Generate:            gen,
		RequestedAt:         reqAt,
		LatencyMs:           item.LatencyMs,
		TTFTMs:              item.TTFTMs,
		Failed:              failed,
		FailureStatusCode:   statusCode,
		FailureBody:         failureBody,
		FailSummary:         failSummary,
		HeaderErrorKind:     item.HeaderErrorKind,
		HeaderErrorCode:     item.HeaderErrorCode,
		HeaderTraceID:       item.HeaderTraceID,
		InputTokens:         item.InputTokens,
		OutputTokens:        item.OutputTokens,
		ReasoningTokens:     item.ReasoningTokens,
		CachedTokens:        item.CachedTokens,
		CacheReadTokens:     item.CacheReadTokens,
		CacheCreationTokens: item.CacheCreationTokens,
		TotalTokens:         totalTokens,
		InputCost:           inputCost,
		OutputCost:          outputCost,
		CacheReadCost:       item.CacheReadCost,
		CacheCreationCost:   item.CacheCreationCost,
		TotalCost:           totalCost,
		MatchedModel:        item.MatchedModel,
	}
}
