package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

	// 8. Test /prices (GET)
	respPrices := handler.Handle(http.MethodGet, "/api/prices", url.Values{"search": {"gpt-4o"}}, nil)
	if respPrices.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for /prices, got %d", respPrices.StatusCode)
	}
	var priceList struct {
		TotalModels int `json:"total_models"`
		Models      []struct {
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(respPrices.Body, &priceList); err != nil {
		t.Fatalf("failed to parse /prices response: %v", err)
	}
	if len(priceList.Models) == 0 {
		t.Errorf("expected to find at least one model matching gpt-4o")
	}

	// 9. Test /prices/override (POST & DELETE)
	overrideBody := []byte(`{
		"model": "my-custom-gpt",
		"prompt_per_m": 1.5,
		"completion_per_m": 6.0
	}`)
	respOverride := handler.Handle(http.MethodPost, "/api/prices/override", url.Values{}, overrideBody)
	if respOverride.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for /prices/override, got %d: %s", respOverride.StatusCode, string(respOverride.Body))
	}

	// Verify the override was applied to pricing engine
	overriddenPrice, matched := pricingEngine.GetModelPricing("my-custom-gpt", time.Now())
	if overriddenPrice == nil || matched != "my-custom-gpt" {
		t.Fatalf("expected my-custom-gpt to be matched in engine")
	}
	if overriddenPrice.InputCostPerToken != 1.5e-6 {
		t.Errorf("expected input 1.5e-6, got %v", overriddenPrice.InputCostPerToken)
	}

	// Delete the override
	respDelOverride := handler.Handle(http.MethodDelete, "/api/prices/override", url.Values{"model": {"my-custom-gpt"}}, nil)
	if respDelOverride.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for delete override, got %d", respDelOverride.StatusCode)
	}

	// 10. Test /prices/sync (POST with custom mock server)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"api-synced-model": {
				"input_cost_per_token": 0.000003,
				"output_cost_per_token": 0.000012
			}
		}`))
	}))
	defer mockServer.Close()

	syncBody := []byte(fmt.Sprintf(`{"source": "litellm", "url": "%s"}`, mockServer.URL))
	respSync := handler.Handle(http.MethodPost, "/api/prices/sync", url.Values{}, syncBody)
	if respSync.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for sync, got %d: %s", respSync.StatusCode, string(respSync.Body))
	}

	// Verify synced model exists
	syncedPrice, _ := pricingEngine.GetModelPricing("api-synced-model", time.Now())
	if syncedPrice == nil {
		t.Fatalf("expected api-synced-model to exist after sync")
	}
	if syncedPrice.InputCostPerToken != 0.000003 {
		t.Errorf("expected 0.000003, got %v", syncedPrice.InputCostPerToken)
	}

	// 11. Test /diagnostics
	respDiag := handler.Handle(http.MethodGet, "/api/diagnostics", url.Values{}, nil)
	if respDiag.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for diagnostics, got %d: %s", respDiag.StatusCode, string(respDiag.Body))
	}
	var diagStats storage.DiagnosticStats
	if err := json.Unmarshal(respDiag.Body, &diagStats); err != nil {
		t.Fatalf("failed to parse diagnostics json: %v", err)
	}
	if diagStats.TotalRequests != 2 {
		t.Errorf("expected 2 total requests in diagnostics, got %d", diagStats.TotalRequests)
	}

	// 12. Test /export (JSONL, CSV, JSON)
	// 12.1 JSONL
	respExpJSONL := handler.Handle(http.MethodGet, "/api/export", url.Values{"format": {"jsonl"}}, nil)
	if respExpJSONL.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for export jsonl, got %d", respExpJSONL.StatusCode)
	}
	if !strings.Contains(string(respExpJSONL.Body), `"model":"gpt-4o"`) {
		t.Errorf("expected exported jsonl to contain gpt-4o")
	}

	// 12.2 CSV
	respExpCSV := handler.Handle(http.MethodGet, "/api/export", url.Values{"format": {"csv"}}, nil)
	if respExpCSV.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for export csv, got %d", respExpCSV.StatusCode)
	}
	csvBody := string(respExpCSV.Body)
	if !strings.HasPrefix(csvBody, "id,requested_at,provider,model") {
		t.Errorf("expected CSV header, got %s", csvBody[:50])
	}
	if !strings.Contains(csvBody, "claude-3-5-sonnet") {
		t.Errorf("expected CSV to contain claude-3-5-sonnet")
	}

	// 12.3 JSON
	respExpJSON := handler.Handle(http.MethodGet, "/api/export", url.Values{"format": {"json"}}, nil)
	if respExpJSON.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for export json, got %d", respExpJSON.StatusCode)
	}
	var exportedArray []storage.Record
	if err := json.Unmarshal(respExpJSON.Body, &exportedArray); err != nil {
		t.Fatalf("failed to parse exported JSON array: %v", err)
	}
	if len(exportedArray) != 2 {
		t.Errorf("expected 2 records exported in JSON array, got %d", len(exportedArray))
	}

	// 13. Test /import (JSONL and JSON array)
	// 13.1 Import JSON array with legacy CPA-Manager-Plus fields (event_hash, timestamp_ms, etc.)
	legacyPayload := []byte(`[
		{
			"event_hash": "legacy-hash-1",
			"timestamp_ms": 1778000000000,
			"model": "gpt-4o",
			"input_tokens": 1000,
			"output_tokens": 500,
			"failed": false
		},
		{
			"event_hash": "legacy-hash-2",
			"timestamp_ms": 1778000005000,
			"model": "gpt-4o",
			"fail_status_code": 429,
			"error": "Rate limit reached"
		}
	]`)

	respImp := handler.Handle(http.MethodPost, "/api/import", url.Values{}, legacyPayload)
	if respImp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for import, got %d: %s", respImp.StatusCode, string(respImp.Body))
	}
	var impRes struct {
		Status   string `json:"status"`
		Format   string `json:"format"`
		Imported int    `json:"imported"`
		Skipped  int    `json:"skipped"`
	}
	if err := json.Unmarshal(respImp.Body, &impRes); err != nil {
		t.Fatalf("failed to parse import response: %v", err)
	}
	if impRes.Imported != 2 || impRes.Skipped != 0 {
		t.Errorf("expected 2 imported, 0 skipped, got %+v", impRes)
	}

	// 13.2 Re-importing the exact same payload should skip both as duplicates
	respImpDup := handler.Handle(http.MethodPost, "/api/import", url.Values{}, legacyPayload)
	if respImpDup.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for duplicate import, got %d", respImpDup.StatusCode)
	}
	var impDupRes struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	_ = json.Unmarshal(respImpDup.Body, &impDupRes)
	if impDupRes.Imported != 0 || impDupRes.Skipped != 2 {
		t.Errorf("expected 0 imported, 2 skipped on duplicate import, got %+v", impDupRes)
	}

	// Verify diagnostics now reflects the newly imported failure
	respDiagAfter := handler.Handle(http.MethodGet, "/api/diagnostics", url.Values{}, nil)
	var diagAfter storage.DiagnosticStats
	_ = json.Unmarshal(respDiagAfter.Body, &diagAfter)
	if diagAfter.TotalRequests != 4 {
		t.Errorf("expected 4 total requests now, got %d", diagAfter.TotalRequests)
	}
	if diagAfter.FailedRequests != 1 {
		t.Errorf("expected 1 failed request, got %d", diagAfter.FailedRequests)
	}

	// 14. Test /accounts/health
	respHealth := handler.Handle(http.MethodGet, "/api/accounts/health", url.Values{}, nil)
	if respHealth.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for accounts health, got %d: %s", respHealth.StatusCode, string(respHealth.Body))
	}
	var healthResp storage.AccountHealthResponse
	if err := json.Unmarshal(respHealth.Body, &healthResp); err != nil {
		t.Fatalf("failed to parse accounts health response: %v", err)
	}
	if healthResp.TotalAccounts == 0 {
		t.Errorf("expected at least 1 account in health response")
	}

	// 15. Test /accounts/quota
	respQuota := handler.Handle(http.MethodGet, "/api/accounts/quota", url.Values{}, nil)
	if respQuota.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for accounts quota, got %d: %s", respQuota.StatusCode, string(respQuota.Body))
	}
	var quotaResp storage.AccountQuotaResponse
	if err := json.Unmarshal(respQuota.Body, &quotaResp); err != nil {
		t.Fatalf("failed to parse accounts quota response: %v", err)
	}
	if quotaResp.TotalAccounts == 0 {
		t.Errorf("expected at least 1 account in quota response")
	}

	// 16. Test Model Mismatch Detection via /records?mismatch=true and /diagnostics
	store.InsertRecord(&storage.Record{
		Provider:      "openai",
		Model:         "gpt-4o",
		ResponseModel: "gpt-4o-mini",
		ModelMismatch: true,
		RequestedAt:   now.Add(-1 * time.Minute),
		LatencyMs:     600,
		TotalTokens:   100,
	})

	respMismatch := handler.Handle(http.MethodGet, "/api/records", url.Values{"mismatch": {"true"}}, nil)
	if respMismatch.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for mismatch filter, got %d", respMismatch.StatusCode)
	}
	var mismatchPage struct {
		Total   int64             `json:"total"`
		Records []*storage.Record `json:"records"`
	}
	if err := json.Unmarshal(respMismatch.Body, &mismatchPage); err != nil {
		t.Fatalf("failed to parse mismatch response: %v", err)
	}
	if mismatchPage.Total != 1 || len(mismatchPage.Records) != 1 {
		t.Fatalf("expected 1 mismatched record, got %d", mismatchPage.Total)
	}
	if !mismatchPage.Records[0].ModelMismatch || mismatchPage.Records[0].ResponseModel != "gpt-4o-mini" {
		t.Errorf("expected ModelMismatch=true and ResponseModel=gpt-4o-mini, got %+v", mismatchPage.Records[0])
	}

	// Verify diagnostics count includes the mismatch
	respDiagMismatch := handler.Handle(http.MethodGet, "/api/diagnostics", url.Values{}, nil)
	var diagMismatch storage.DiagnosticStats
	_ = json.Unmarshal(respDiagMismatch.Body, &diagMismatch)
	if diagMismatch.ModelMismatches != 1 {
		t.Errorf("expected 1 model mismatch in diagnostics, got %d", diagMismatch.ModelMismatches)
	}
}
