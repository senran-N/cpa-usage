package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test_usage.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}

	now := time.Now()

	// 1. Test InsertRecord
	rec1 := &Record{
		Provider:     "openai",
		Model:        "gpt-4o",
		APIKey:       "sk-client-key-1",
		AuthID:       "openai-cred-1",
		AuthType:     "api_key",
		RequestedAt:  now.Add(-2 * time.Hour),
		LatencyMs:    1200,
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		InputCost:    0.0025,
		OutputCost:   0.0050,
		TotalCost:    0.0075,
		MatchedModel: "gpt-4o",
	}
	if err := store.InsertRecord(rec1); err != nil {
		t.Fatalf("failed to insert record 1: %v", err)
	}
	if rec1.ID == 0 {
		t.Errorf("expected non-zero ID for record 1")
	}

	// 2. Test BatchInsertRecords
	batch := []*Record{
		{
			Provider:        "anthropic",
			Model:           "claude-3-5-sonnet",
			APIKey:          "sk-client-key-2",
			AuthID:          "claude-cred-1",
			AuthType:        "setup_token",
			RequestedAt:     now.Add(-1 * time.Hour),
			LatencyMs:       2500,
			InputTokens:     2000,
			OutputTokens:    800,
			CacheReadTokens: 1000,
			TotalTokens:     2800,
			InputCost:       0.006,
			OutputCost:      0.012,
			CacheReadCost:   0.0003,
			TotalCost:       0.0183,
			MatchedModel:    "claude-3-5-sonnet",
		},
		{
			Provider:          "deepseek",
			Model:             "deepseek-chat",
			APIKey:            "sk-client-key-1",
			AuthID:            "deepseek-cred-1",
			AuthType:          "api_key",
			RequestedAt:       now,
			LatencyMs:         800,
			Failed:            true,
			FailureStatusCode: 500,
			FailureBody:       "upstream error",
			TotalTokens:       0,
			TotalCost:         0,
		},
	}
	if err := store.BatchInsertRecords(batch); err != nil {
		t.Fatalf("failed to batch insert: %v", err)
	}

	// 3. Test Ingest (Queue)
	store.Ingest(&Record{
		Provider:     "openai",
		Model:        "gpt-4o-mini",
		APIKey:       "sk-client-key-3",
		RequestedAt:  now,
		LatencyMs:    400,
		InputTokens:  500,
		OutputTokens: 200,
		TotalTokens:  700,
		TotalCost:    0.0002,
	})
	// Wait a moment for queue to flush
	time.Sleep(350 * time.Millisecond)

	// 4. Test GetSummary
	summary, err := store.GetSummary(QueryFilter{})
	if err != nil {
		t.Fatalf("failed to get summary: %v", err)
	}
	if summary.TotalRequests != 4 {
		t.Errorf("expected 4 total requests, got %d", summary.TotalRequests)
	}
	if summary.SuccessRequests != 3 {
		t.Errorf("expected 3 success requests, got %d", summary.SuccessRequests)
	}
	if summary.FailedRequests != 1 {
		t.Errorf("expected 1 failed request, got %d", summary.FailedRequests)
	}
	if summary.TotalCost <= 0 {
		t.Errorf("expected positive total cost, got %f", summary.TotalCost)
	}

	// 5. Test Filtered Summary
	openAISummary, err := store.GetSummary(QueryFilter{Provider: "openai"})
	if err != nil {
		t.Fatalf("failed to get openai summary: %v", err)
	}
	if openAISummary.TotalRequests != 2 {
		t.Errorf("expected 2 openai requests, got %d", openAISummary.TotalRequests)
	}

	// 6. Test TimeSeries
	ts, err := store.GetTimeSeries(QueryFilter{}, "hour")
	if err != nil {
		t.Fatalf("failed to get time series: %v", err)
	}
	if len(ts) == 0 {
		t.Errorf("expected time series points, got 0")
	}

	// 7. Test ModelStats
	mStats, err := store.GetModelStats(QueryFilter{})
	if err != nil {
		t.Fatalf("failed to get model stats: %v", err)
	}
	if len(mStats) != 4 {
		t.Errorf("expected 4 models, got %d", len(mStats))
	}

	// 8. Test APIKeyStats
	kStats, err := store.GetAPIKeyStats(QueryFilter{})
	if err != nil {
		t.Fatalf("failed to get api key stats: %v", err)
	}
	if len(kStats) != 3 {
		t.Errorf("expected 3 api keys, got %d", len(kStats))
	}
	// sk-client-key-1 should have 2 requests
	var key1Requests int64
	for _, k := range kStats {
		if k.APIKey == "sk-client-key-1" {
			key1Requests = k.Requests
		}
	}
	if key1Requests != 2 {
		t.Errorf("expected sk-client-key-1 to have 2 requests, got %d", key1Requests)
	}

	// 9. Test AuthStats
	aStats, err := store.GetAuthStats(QueryFilter{})
	if err != nil {
		t.Fatalf("failed to get auth stats: %v", err)
	}
	if len(aStats) != 3 {
		t.Errorf("expected 3 auth credentials, got %d", len(aStats))
	}

	// 10. Test GetRecords Pagination
	recs, total, err := store.GetRecords(RecordQueryFilter{
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("failed to get records: %v", err)
	}
	if total != 4 {
		t.Errorf("expected total 4, got %d", total)
	}
	if len(recs) != 2 {
		t.Errorf("expected page size 2, got %d", len(recs))
	}

	// 11. Test FilterOptions
	opts, err := store.GetFilterOptions()
	if err != nil {
		t.Fatalf("failed to get filter options: %v", err)
	}
	if len(opts.Models) < 3 || len(opts.Providers) < 3 || len(opts.APIKeys) < 3 {
		t.Errorf("expected options populated, got %+v", opts)
	}

	// 12. Test Cleanup
	// Delete records older than 90 minutes ago (should delete rec1)
	deleted, err := store.Cleanup(now.Add(-90 * time.Minute))
	if err != nil {
		t.Fatalf("failed to cleanup: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted record, got %d", deleted)
	}

	// 13. Test Close
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}
}
