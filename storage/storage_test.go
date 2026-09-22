package storage

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFlushMakesQueuedRecordsImmediatelyReadable(t *testing.T) {
	tempDir := t.TempDir()
	store, err := Open(filepath.Join(tempDir, "flush.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Ingest(&Record{Provider: "openai", Model: "gpt-4o", RequestedAt: time.Now(), Generate: true})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	stats, err := store.GetSummary(QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", stats.TotalRequests)
	}
}

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

func TestCustomAndSyncedPricesStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-price-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test_prices.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	// 1. Custom prices
	cp := CustomPriceRecord{
		Model:                 "custom-gpt-4o",
		InputCostPerToken:     2.0e-6,
		OutputCostPerToken:    8.0e-6,
		CacheReadCostPerToken: 1.0e-6,
		SupportsPromptCaching: true,
		Source:                "manual",
		UpdatedAt:             time.Now().Unix(),
	}
	if err := store.SaveCustomPrice(cp); err != nil {
		t.Fatalf("failed to save custom price: %v", err)
	}

	customs, err := store.LoadCustomPrices()
	if err != nil {
		t.Fatalf("failed to load custom prices: %v", err)
	}
	if len(customs) != 1 || customs[0].Model != "custom-gpt-4o" {
		t.Fatalf("expected custom-gpt-4o loaded, got %+v", customs)
	}

	// 2. Synced prices batch
	syncedBatch := []SyncedPriceRecord{
		{
			Model:              "synced-claude-3-5",
			InputCostPerToken:  3.0e-6,
			OutputCostPerToken: 1.5e-5,
			Source:             "litellm",
			UpdatedAt:          time.Now().Unix(),
		},
		{
			Model:              "synced-gemini-2.0",
			InputCostPerToken:  1.0e-7,
			OutputCostPerToken: 4.0e-7,
			Source:             "openrouter",
			UpdatedAt:          time.Now().Unix(),
		},
	}
	if err := store.SaveSyncedPricesBatch(syncedBatch); err != nil {
		t.Fatalf("failed to save synced prices batch: %v", err)
	}

	syncedLoaded, err := store.LoadSyncedPrices()
	if err != nil {
		t.Fatalf("failed to load synced prices: %v", err)
	}
	if len(syncedLoaded) != 2 {
		t.Fatalf("expected 2 synced prices, got %d", len(syncedLoaded))
	}

	// 3. Delete custom price
	if err := store.DeleteCustomPrice("custom-gpt-4o"); err != nil {
		t.Fatalf("failed to delete custom price: %v", err)
	}
	customsAfter, err := store.LoadCustomPrices()
	if err != nil {
		t.Fatalf("failed to load custom prices after delete: %v", err)
	}
	if len(customsAfter) != 0 {
		t.Fatalf("expected 0 custom prices after delete, got %d", len(customsAfter))
	}
}

func TestDiagnosticsAndImportExport(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-diag-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "diag_test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	now := time.Now()

	// Insert 2 successful and 2 failed records
	recs := []*Record{
		{
			Provider:     "openai",
			Model:        "gpt-4o",
			APIKey:       "key-1",
			AuthID:       "auth-oa-1",
			SessionID:    "sess-001",
			RequestedAt:  now.Add(-20 * time.Minute),
			LatencyMs:    400,
			InputTokens:  1000,
			OutputTokens: 500,
			TotalTokens:  1500,
			TotalCost:    0.005,
		},
		{
			Provider:     "anthropic",
			Model:        "claude-3-5-sonnet",
			APIKey:       "key-2",
			AuthID:       "auth-an-1",
			SessionID:    "sess-002",
			RequestedAt:  now.Add(-15 * time.Minute),
			LatencyMs:    600,
			InputTokens:  2000,
			OutputTokens: 1000,
			TotalTokens:  3000,
			TotalCost:    0.015,
		},
		{
			Provider:          "openai",
			Model:             "gpt-4o",
			APIKey:            "key-1",
			AuthID:            "auth-oa-1",
			SessionID:         "sess-003",
			RequestedAt:       now.Add(-10 * time.Minute),
			LatencyMs:         150,
			Failed:            true,
			FailureStatusCode: 429,
			HeaderErrorKind:   "insufficient_quota",
			HeaderErrorCode:   "quota_exhausted",
			FailSummary:       "Quota exceeded for account",
			TotalTokens:       0,
		},
		{
			Provider:          "openai",
			Model:             "gpt-4o-mini",
			APIKey:            "key-1",
			AuthID:            "auth-oa-2",
			SessionID:         "sess-004",
			RequestedAt:       now.Add(-5 * time.Minute),
			LatencyMs:         200,
			Failed:            true,
			FailureStatusCode: 401,
			HeaderErrorKind:   "invalid_api_key",
			HeaderErrorCode:   "unauthorized",
			FailSummary:       "Invalid key provided",
			TotalTokens:       0,
		},
	}

	if err := store.BatchInsertRecords(recs); err != nil {
		t.Fatalf("failed to insert records: %v", err)
	}

	// 1. Test GetDiagnosticStats
	diag, err := store.GetDiagnosticStats(QueryFilter{}, 10)
	if err != nil {
		t.Fatalf("GetDiagnosticStats failed: %v", err)
	}
	if diag.TotalRequests != 4 {
		t.Errorf("expected 4 total requests, got %d", diag.TotalRequests)
	}
	if diag.FailedRequests != 2 {
		t.Errorf("expected 2 failed requests, got %d", diag.FailedRequests)
	}
	if diag.FailureRate != 0.5 {
		t.Errorf("expected failure rate 0.5, got %v", diag.FailureRate)
	}
	if len(diag.ByStatusCode) != 2 {
		t.Errorf("expected 2 status code groups (429 and 401), got %d", len(diag.ByStatusCode))
	}
	if len(diag.ByErrorKind) != 2 {
		t.Errorf("expected 2 error kind groups, got %d", len(diag.ByErrorKind))
	}
	if len(diag.RecentErrors) != 2 {
		t.Errorf("expected 2 recent errors, got %d", len(diag.RecentErrors))
	}

	// 2. Test ExportRecords
	exported, err := store.ExportRecords(QueryFilter{}, 100)
	if err != nil {
		t.Fatalf("ExportRecords failed: %v", err)
	}
	if len(exported) != 4 {
		t.Errorf("expected 4 exported records, got %d", len(exported))
	}

	// Export with filter
	failedVal := true
	exportedFailed, err := store.ExportRecords(QueryFilter{Failed: &failedVal}, 100)
	if err != nil {
		t.Fatalf("ExportRecords with failed filter failed: %v", err)
	}
	if len(exportedFailed) != 2 {
		t.Errorf("expected 2 failed records exported, got %d", len(exportedFailed))
	}

	// 3. Test ImportRecords with deduplication
	// Create duplicate of sess-001, plus a new record sess-005
	importBatch := []*Record{
		{
			SessionID:   "sess-001", // duplicate
			Provider:    "openai",
			Model:       "gpt-4o",
			RequestedAt: now.Add(-20 * time.Minute),
			TotalTokens: 1500,
		},
		{
			SessionID:   "sess-005", // new
			Provider:    "google",
			Model:       "gemini-2.0-flash",
			APIKey:      "key-gem",
			AuthID:      "auth-google-1",
			RequestedAt: now.Add(-1 * time.Minute),
			TotalTokens: 500,
			TotalCost:   0.0001,
		},
	}

	imported, skipped, err := store.ImportRecords(importBatch)
	if err != nil {
		t.Fatalf("ImportRecords failed: %v", err)
	}
	if imported != 1 {
		t.Errorf("expected 1 imported, got %d", imported)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}

	// Verify total count in DB is now 5
	diagAfter, _ := store.GetDiagnosticStats(QueryFilter{}, 10)
	if diagAfter.TotalRequests != 5 {
		t.Errorf("expected 5 total requests after import, got %d", diagAfter.TotalRequests)
	}
}

func TestDetectModelMismatch(t *testing.T) {
	cases := []struct {
		requested string
		resolved  string
		response  string
		expected  bool
	}{
		{"gpt-4o", "gpt-4o", "gpt-4o", false},
		{"gpt-4o", "gpt-4o", "gpt-4o-2024-08-06", false},
		{"claude-3-5-sonnet-latest", "claude-3-5-sonnet", "claude-3-5-sonnet-20241022", false},
		{"models/gemini-2.0-flash", "gemini-2.0-flash", "gemini-2.0-flash", false},
		{"gpt-4o", "gpt-4o", "gpt-4o-mini", true},
		{"claude-3-5-sonnet", "claude-3-5-sonnet", "claude-3-5-haiku", true},
		{"gemini-1.5-pro", "gemini-1.5-pro", "gemini-1.5-flash", true},
		{"deepseek-reasoner", "deepseek-reasoner", "deepseek-chat", true},
		{"gpt-4o", "gpt-4o", "gpt-3.5-turbo", true},
		{"", "gpt-4o", "", false},
	}

	for _, c := range cases {
		got := DetectModelMismatch(c.requested, c.resolved, c.response)
		if got != c.expected {
			t.Errorf("DetectModelMismatch(%q, %q, %q) = %v; want %v", c.requested, c.resolved, c.response, got, c.expected)
		}
	}
}

func TestFlushFastPathWhenIdle(t *testing.T) {
	tempDir := t.TempDir()
	store, err := Open(filepath.Join(tempDir, "idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Flush on a store with nothing pending must return immediately without
	// hitting the worker, and repeated calls must stay cheap.
	for i := 0; i < 100; i++ {
		if err := store.Flush(); err != nil {
			t.Fatalf("idle flush %d failed: %v", i, err)
		}
	}
	if got := store.unflushed.Load(); got != 0 {
		t.Fatalf("unflushed = %d, want 0", got)
	}
}

func TestFlushUnflushedCounterConsistency(t *testing.T) {
	tempDir := t.TempDir()
	store, err := Open(filepath.Join(tempDir, "counter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const n = 250 // > batchFlushSize so several commits happen
	for i := 0; i < n; i++ {
		store.Ingest(&Record{Provider: "openai", Model: "gpt-4o", RequestedAt: time.Now()})
	}
	if got := store.unflushed.Load(); got != int64(n) {
		t.Fatalf("unflushed = %d, want %d", got, n)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := store.unflushed.Load(); got != 0 {
		t.Fatalf("unflushed after flush = %d, want 0", got)
	}

	// Concurrent flushers must not corrupt the counter or lose visibility.
	for i := 0; i < n; i++ {
		store.Ingest(&Record{Provider: "openai", Model: "gpt-4o", RequestedAt: time.Now()})
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Flush(); err != nil {
				t.Errorf("concurrent flush: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := store.unflushed.Load(); got != 0 {
		t.Fatalf("unflushed after concurrent flushes = %d, want 0", got)
	}
	stats, err := store.GetSummary(QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalRequests != int64(2*n) {
		t.Fatalf("total requests = %d, want %d", stats.TotalRequests, 2*n)
	}
}

func TestGetTimeSeriesBucketFormats(t *testing.T) {
	tempDir := t.TempDir()
	store, err := Open(filepath.Join(tempDir, "ts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Fixed UTC times so the expected bucket strings are exact.
	base := time.Date(2026, 3, 30, 14, 23, 45, 0, time.UTC)
	for _, delta := range []time.Duration{0, -30 * time.Minute, -25 * time.Hour} {
		if err := store.InsertRecord(&Record{Provider: "openai", Model: "gpt-4o", RequestedAt: base.Add(delta)}); err != nil {
			t.Fatal(err)
		}
	}

	hourPts, err := store.GetTimeSeries(QueryFilter{}, "hour")
	if err != nil {
		t.Fatal(err)
	}
	// 14:00 bucket and 13:00 bucket (yesterday 13:00 for -25h => 2026-03-29 13:00)
	if len(hourPts) != 3 {
		t.Fatalf("hour buckets = %d, want 3: %+v", len(hourPts), hourPts)
	}
	if hourPts[0].Timestamp != "2026-03-29 13:00" || hourPts[1].Timestamp != "2026-03-30 13:00" || hourPts[2].Timestamp != "2026-03-30 14:00" {
		t.Fatalf("hour timestamps = %v/%v/%v", hourPts[0].Timestamp, hourPts[1].Timestamp, hourPts[2].Timestamp)
	}
	if hourPts[2].Requests != 1 {
		t.Fatalf("14:00 bucket requests = %d, want 1", hourPts[2].Requests)
	}

	dayPts, err := store.GetTimeSeries(QueryFilter{}, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(dayPts) != 2 || dayPts[0].Timestamp != "2026-03-29" || dayPts[1].Timestamp != "2026-03-30" {
		t.Fatalf("day buckets = %+v", dayPts)
	}

	minPts, err := store.GetTimeSeries(QueryFilter{}, "minute")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range minPts {
		if p.Timestamp == "2026-03-30 14:23" {
			found = true
		}
	}
	if !found {
		t.Fatalf("minute buckets missing 14:23: %+v", minPts)
	}
}

func TestGetFilterOptionsCachedAndInvalidated(t *testing.T) {
	tempDir := t.TempDir()
	store, err := Open(filepath.Join(tempDir, "fo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.InsertRecord(&Record{Provider: "openai", Model: "gpt-4o", APIKey: "k1", AuthID: "a1", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	opts1, err := store.GetFilterOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts1.Models) != 1 || opts1.Models[0] != "gpt-4o" {
		t.Fatalf("models = %v", opts1.Models)
	}

	// Second call must hit the cache and return the identical slice content.
	opts2, err := store.GetFilterOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts2.Models) != 1 || opts2.Models[0] != "gpt-4o" {
		t.Fatalf("cached models = %v", opts2.Models)
	}
	if store.filterOptsCached == nil {
		t.Fatal("expected cache to be populated")
	}

	// A write within the TTL does not invalidate (TTL-based freshness), but a
	// bulk delete must.
	if err := store.InsertRecord(&Record{Provider: "anthropic", Model: "claude-3", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearAll(); err != nil {
		t.Fatal(err)
	}
	opts3, err := store.GetFilterOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts3.Models) != 0 {
		t.Fatalf("models after clear = %v", opts3.Models)
	}
}
