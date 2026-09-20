package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage/pricing"
	"cpa-usage/storage"
)

func TestAPIEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "api_test.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	pricingEngine := pricing.Default()
	handler := NewHandler(store, pricingEngine)

	// Seed some test records
	now := time.Now()
	store.InsertRecord(&storage.Record{
		Provider:     "openai",
		Model:        "gpt-4o",
		APIKey:       "key-alpha",
		AuthID:       "auth-1",
		RequestedAt:  now.Add(-10 * time.Minute),
		LatencyMs:    500,
		InputTokens:  1000,
		OutputTokens: 200,
		TotalTokens:  1200,
		InputCost:    0.0025,
		OutputCost:   0.0020,
		TotalCost:    0.0045,
	})
	store.InsertRecord(&storage.Record{
		Provider:     "anthropic",
		Model:        "claude-3-5-sonnet",
		APIKey:       "key-beta",
		AuthID:       "auth-2",
		RequestedAt:  now.Add(-5 * time.Minute),
		LatencyMs:    1500,
		InputTokens:  2000,
		OutputTokens: 1000,
		TotalTokens:  3000,
		InputCost:    0.006,
		OutputCost:   0.015,
		TotalCost:    0.021,
	})

	// 1. Test /summary
	respSummary := handler.Handle(http.MethodGet, "/api/summary", url.Values{}, nil)
	if respSummary.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", respSummary.StatusCode, string(respSummary.Body))
	}
	var summary storage.SummaryStats
	if err := json.Unmarshal(respSummary.Body, &summary); err != nil {
		t.Fatalf("failed to parse summary json: %v", err)
	}
	if summary.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 4200 {
		t.Errorf("expected 4200 total tokens, got %d", summary.TotalTokens)
	}

	// 2. Test /timeseries
	respTS := handler.Handle(http.MethodGet, "/api/timeseries", url.Values{"interval": {"hour"}}, nil)
	if respTS.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respTS.StatusCode)
	}
	var points []*storage.TimeSeriesPoint
	if err := json.Unmarshal(respTS.Body, &points); err != nil {
		t.Fatalf("failed to parse timeseries json: %v", err)
	}
	if len(points) == 0 {
		t.Errorf("expected at least 1 timeseries point")
	}

	// 3. Test /models
	respModels := handler.Handle(http.MethodGet, "/api/models", url.Values{}, nil)
	if respModels.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respModels.StatusCode)
	}
	var models []*storage.ModelStat
	if err := json.Unmarshal(respModels.Body, &models); err != nil {
		t.Fatalf("failed to parse models json: %v", err)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// 4. Test /keys
	respKeys := handler.Handle(http.MethodGet, "/api/keys", url.Values{}, nil)
	if respKeys.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respKeys.StatusCode)
	}
	var keys []*storage.APIKeyStat
	if err := json.Unmarshal(respKeys.Body, &keys); err != nil {
		t.Fatalf("failed to parse keys json: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 api keys, got %d", len(keys))
	}

	// 5. Test /records
	respRecords := handler.Handle(http.MethodGet, "/api/records", url.Values{"page": {"1"}, "page_size": {"10"}}, nil)
	if respRecords.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respRecords.StatusCode)
	}
	var recPage struct {
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
		Total    int64             `json:"total"`
		Records  []*storage.Record `json:"records"`
	}
	if err := json.Unmarshal(respRecords.Body, &recPage); err != nil {
		t.Fatalf("failed to parse records json: %v", err)
	}
	if recPage.Total != 2 || len(recPage.Records) != 2 {
		t.Errorf("expected 2 records, got total=%d len=%d", recPage.Total, len(recPage.Records))
	}

	// 6. Test /filter-options
	respOpts := handler.Handle(http.MethodGet, "/api/filter-options", url.Values{}, nil)
	if respOpts.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respOpts.StatusCode)
	}
	var opts storage.FilterOptions
	if err := json.Unmarshal(respOpts.Body, &opts); err != nil {
		t.Fatalf("failed to parse filter options: %v", err)
	}
	if len(opts.Models) != 2 || len(opts.Providers) != 2 {
		t.Errorf("expected 2 models and 2 providers in filter options, got %+v", opts)
	}

	// 7. Test /cleanup
	respCleanup := handler.Handle(http.MethodPost, "/api/cleanup", url.Values{"days": {"30"}}, nil)
	if respCleanup.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for cleanup, got %d", respCleanup.StatusCode)
	}
}
