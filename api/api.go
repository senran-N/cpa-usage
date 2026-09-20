package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-usage/pricing"
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
		EndTime:   parseTimeParam(query.Get("end_time")),
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
	return filter
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

	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if pageSize < 1 {
		pageSize = 20
	}
	filter.PageSize = pageSize

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
