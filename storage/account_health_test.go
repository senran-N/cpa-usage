package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvaluateAccountHealth(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()

	// 1. Available account
	stat1 := &AuthStat{
		AuthID:       "acc-healthy",
		Provider:     "codex",
		AuthType:     "oauth",
		Requests:     50,
		FailedCount:  0,
		AvgLatencyMs: 350,
	}
	usedPct1 := 45.0
	rec1 := &Record{
		AuthID:                 "acc-healthy",
		Provider:               "codex",
		RequestedAt:            now.Add(-2 * time.Minute),
		HeaderQuotaUsedPercent: &usedPct1,
	}
	h1 := EvaluateAccountHealth(stat1, rec1, now)
	if h1.Status != HealthStatusAvailable {
		t.Errorf("expected status available, got %s", h1.Status)
	}
	if h1.HealthScore < 95.0 {
		t.Errorf("expected health score >= 95, got %v", h1.HealthScore)
	}
	if h1.InCooldown {
		t.Errorf("expected not in cooldown")
	}

	// 2. Active Cooldown (5H window)
	futureRecoverMS := nowMS + 7200*1000 // 2 hours in future
	usedPct2 := 95.0
	stat2 := &AuthStat{
		AuthID:      "acc-cooldown",
		Provider:    "codex",
		Requests:    20,
		FailedCount: 1,
	}
	rec2 := &Record{
		AuthID:                 "acc-cooldown",
		Provider:               "codex",
		RequestedAt:            now.Add(-1 * time.Minute),
		Failed:                 true,
		FailureStatusCode:      429,
		HeaderQuotaRecoverAtMS: futureRecoverMS,
		HeaderQuotaUsedPercent: &usedPct2,
	}
	h2 := EvaluateAccountHealth(stat2, rec2, now)
	if h2.Status != HealthStatusCooldown {
		t.Errorf("expected status cooldown, got %s", h2.Status)
	}
	if !h2.InCooldown {
		t.Errorf("expected InCooldown true")
	}
	if h2.CooldownRemainingSeconds != 7200 {
		t.Errorf("expected cooldown remaining 7200s, got %d", h2.CooldownRemainingSeconds)
	}
	if h2.HealthScore > 35.0 {
		t.Errorf("expected health score capped at 35, got %v", h2.HealthScore)
	}

	// 3. Past Cooldown timestamp (should NOT be in cooldown)
	pastRecoverMS := nowMS - 10000 // 10s in past
	rec3 := &Record{
		AuthID:                 "acc-past-cooldown",
		Provider:               "codex",
		RequestedAt:            now.Add(-30 * time.Second),
		HeaderQuotaRecoverAtMS: pastRecoverMS,
	}
	h3 := EvaluateAccountHealth(stat1, rec3, now)
	if h3.InCooldown {
		t.Errorf("expected InCooldown false for past recover timestamp")
	}
	if h3.CooldownRemainingSeconds != 0 {
		t.Errorf("expected CooldownRemainingSeconds 0, got %d", h3.CooldownRemainingSeconds)
	}

	// 4. Exhausted account (100% quota used)
	usedPct4 := 100.0
	rec4 := &Record{
		AuthID:                 "acc-exhausted",
		Provider:               "codex",
		RequestedAt:            now.Add(-5 * time.Minute),
		HeaderQuotaUsedPercent: &usedPct4,
	}
	h4 := EvaluateAccountHealth(stat1, rec4, now)
	if h4.Status != HealthStatusExhausted {
		t.Errorf("expected status exhausted, got %s", h4.Status)
	}
	if h4.HealthScore > 15.0 {
		t.Errorf("expected health score <= 15 for exhausted, got %v", h4.HealthScore)
	}

	// 5. Negative and extreme quota percent handling (clamping robustness)
	negPct := -15.5
	rec5 := &Record{
		AuthID:                 "acc-clamped",
		Provider:               "codex",
		RequestedAt:            now.Add(-10 * time.Minute),
		HeaderQuotaUsedPercent: &negPct,
	}
	h5 := EvaluateAccountHealth(stat1, rec5, now)
	if h5.QuotaUsedPercent == nil || *h5.QuotaUsedPercent != 0.0 {
		t.Errorf("expected negative percent clamped to 0, got %v", h5.QuotaUsedPercent)
	}
	for _, invalid := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		rec5.HeaderQuotaUsedPercent = &invalid
		hInvalid := EvaluateAccountHealth(stat1, rec5, now)
		if hInvalid.QuotaUsedPercent == nil || *hInvalid.QuotaUsedPercent != 0.0 {
			t.Errorf("expected non-finite percent clamped to 0, got %v", hInvalid.QuotaUsedPercent)
		}
	}

	// 6. Reauth needed (401 / invalid_grant / token_expired)
	stat6 := &AuthStat{
		AuthID:      "acc-reauth",
		Provider:    "openai",
		Requests:    10,
		FailedCount: 2,
	}
	rec6 := &Record{
		AuthID:            "acc-reauth",
		Provider:          "openai",
		RequestedAt:       now.Add(-1 * time.Minute),
		Failed:            true,
		FailureStatusCode: 401,
		HeaderErrorCode:   "invalid_api_key",
		FailSummary:       "Incorrect API key provided",
	}
	h6 := EvaluateAccountHealth(stat6, rec6, now)
	if h6.Status != HealthStatusReauthNeeded {
		t.Errorf("expected status reauth_needed, got %s", h6.Status)
	}
	if h6.HealthScore != 0.0 {
		t.Errorf("expected health score 0 for reauth_needed, got %v", h6.HealthScore)
	}

	// 7. Error status (server 500 error)
	rec7 := &Record{
		AuthID:            "acc-error",
		Provider:          "anthropic",
		RequestedAt:       now.Add(-1 * time.Minute),
		Failed:            true,
		FailureStatusCode: 502,
		HeaderErrorKind:   "server_error",
		FailSummary:       "Bad Gateway",
	}
	h7 := EvaluateAccountHealth(stat1, rec7, now)
	if h7.Status != HealthStatusError {
		t.Errorf("expected status error, got %s", h7.Status)
	}
}

func TestEvaluateAccountQuota(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()

	primaryUsed := 80.0
	primaryReset := nowMS + 3600*1000 // 1 hour
	secUsed := 40.0
	secReset := nowMS + 86400*1000 // 1 day

	stat := &AuthStat{
		AuthID:      "acc-quota-test",
		Provider:    "codex",
		TotalTokens: 50000,
		TotalCost:   0.15,
	}
	rec := &Record{
		AuthID:                          "acc-quota-test",
		Provider:                        "codex",
		PlanType:                        "team",
		RequestedAt:                     now.Add(-1 * time.Minute),
		HeaderQuotaUsedPercent:          &primaryUsed,
		HeaderQuotaRecoverAtMS:          primaryReset,
		HeaderSecondaryQuotaUsedPercent: &secUsed,
		HeaderSecondaryQuotaRecoverAtMS: secReset,
	}

	q := EvaluateAccountQuota(stat, rec, now)

	if q.PlanType != "team" {
		t.Errorf("expected plan_type team, got %s", q.PlanType)
	}
	if q.PrimaryWindow == nil {
		t.Fatalf("expected PrimaryWindow not nil")
	}
	if q.PrimaryWindow.WindowKind != "five_hour" || q.PrimaryWindow.DurationSeconds != 18000 {
		t.Errorf("expected legacy primary five_hour/18000, got %s/%d", q.PrimaryWindow.WindowKind, q.PrimaryWindow.DurationSeconds)
	}
	if *q.PrimaryWindow.UsedPercent != 80.0 {
		t.Errorf("expected primary used 80, got %v", *q.PrimaryWindow.UsedPercent)
	}
	if *q.PrimaryWindow.RemainingPercent != 20.0 {
		t.Errorf("expected primary remaining 20, got %v", *q.PrimaryWindow.RemainingPercent)
	}
	if q.PrimaryWindow.ResetAfterSec != 3600 {
		t.Errorf("expected primary reset after 3600s, got %d", q.PrimaryWindow.ResetAfterSec)
	}
	if q.PrimaryWindow.IsExhausted {
		t.Errorf("expected primary window not exhausted")
	}

	if q.SecondaryWindow == nil {
		t.Fatalf("expected SecondaryWindow not nil")
	}
	if q.SecondaryWindow.WindowKind != "weekly" || q.SecondaryWindow.DurationSeconds != 604800 {
		t.Errorf("expected legacy secondary weekly/604800, got %s/%d", q.SecondaryWindow.WindowKind, q.SecondaryWindow.DurationSeconds)
	}
	if *q.SecondaryWindow.UsedPercent != 40.0 {
		t.Errorf("expected secondary used 40, got %v", *q.SecondaryWindow.UsedPercent)
	}
	if *q.SecondaryWindow.RemainingPercent != 60.0 {
		t.Errorf("expected secondary remaining 60, got %v", *q.SecondaryWindow.RemainingPercent)
	}

	if q.SummaryUsedPercent == nil || *q.SummaryUsedPercent != 80.0 {
		t.Errorf("expected summary used percent 80, got %v", q.SummaryUsedPercent)
	}
	// A future reset timestamp is the ordinary rolling-window reset. Without
	// rate-limit evidence on the record it must not be reported as a cooldown.
	if q.InCooldown || q.CooldownRemainingSeconds != 0 {
		t.Errorf("window reset mistaken for cooldown: in_cooldown=%v remaining=%d", q.InCooldown, q.CooldownRemainingSeconds)
	}
	if q.ResetRemainingSeconds != 3600 {
		t.Errorf("expected reset countdown 3600s, got %d", q.ResetRemainingSeconds)
	}
}

func TestGetAccountsQuotaIncludesForecastFromStoredObservations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-forecast-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := Open(filepath.Join(tempDir, "forecast.db"))
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	records := make([]*Record, 0, 3)
	for i, used := range []float64{20, 35, 50} {
		usedPtr := used
		minutes := 300.0
		records = append(records, &Record{
			AuthID:                   "forecast-auth",
			Provider:                 "codex",
			Model:                    "gpt-5-codex",
			RequestedAt:              now.Add(time.Duration(i-3) * time.Hour),
			HeaderQuotaRecoverAtMS:   resetAt,
			HeaderQuotaUsedPercent:   &usedPtr,
			HeaderQuotaWindowMinutes: &minutes,
		})
	}
	if err := store.BatchInsertRecords(records); err != nil {
		t.Fatalf("failed to insert forecast records: %v", err)
	}

	response, err := store.GetAccountsQuota(QueryFilter{}, now)
	if err != nil {
		t.Fatalf("GetAccountsQuota failed: %v", err)
	}
	if len(response.Quotas) != 1 || response.Quotas[0].PrimaryWindow == nil {
		t.Fatalf("quota response = %#v", response)
	}
	forecast := response.Quotas[0].PrimaryWindow.Forecast
	if forecast == nil || forecast.Status != "available" || forecast.SampleCount != 3 {
		t.Fatalf("forecast = %#v", forecast)
	}
}

func TestStorageAccountsHealthAndQuota(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-health-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "health_test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	cooldownRecoverMS := now.Add(30 * time.Minute).UnixMilli()

	records := []*Record{
		// Account 1: Healthy
		{
			AuthID:      "auth-1",
			Provider:    "openai",
			Model:       "gpt-4o",
			RequestedAt: now.Add(-10 * time.Minute),
			TotalTokens: 1000,
			TotalCost:   0.005,
		},
		{
			AuthID:      "auth-1",
			Provider:    "openai",
			Model:       "gpt-4o",
			RequestedAt: now.Add(-5 * time.Minute),
			TotalTokens: 1200,
			TotalCost:   0.006,
		},
		// Account 2: Rate limited / Cooldown
		{
			AuthID:                 "auth-2",
			Provider:               "codex",
			Model:                  "gpt-4o",
			PlanType:               "plus",
			RequestedAt:            now.Add(-2 * time.Minute),
			Failed:                 true,
			FailureStatusCode:      429,
			HeaderErrorKind:        "rate_limit",
			HeaderQuotaRecoverAtMS: cooldownRecoverMS,
			TotalTokens:            0,
		},
		// Account 3: Reauth needed
		{
			AuthID:            "auth-3",
			Provider:          "anthropic",
			Model:             "claude-3-5-sonnet",
			RequestedAt:       now.Add(-1 * time.Minute),
			Failed:            true,
			FailureStatusCode: 401,
			HeaderErrorKind:   "authentication",
			HeaderErrorCode:   "invalid_api_key",
			FailSummary:       "Invalid authentication header",
			TotalTokens:       0,
		},
	}

	if err := store.BatchInsertRecords(records); err != nil {
		t.Fatalf("failed to insert test records: %v", err)
	}

	// 1. GetAccountsHealth
	healthResp, err := store.GetAccountsHealth(QueryFilter{}, now)
	if err != nil {
		t.Fatalf("GetAccountsHealth failed: %v", err)
	}
	if healthResp.TotalAccounts != 3 {
		t.Fatalf("expected 3 accounts, got %d", healthResp.TotalAccounts)
	}
	if healthResp.Summary.HealthyCount != 1 {
		t.Errorf("expected 1 healthy account, got %d", healthResp.Summary.HealthyCount)
	}
	if healthResp.Summary.CooldownCount != 1 {
		t.Errorf("expected 1 cooldown account, got %d", healthResp.Summary.CooldownCount)
	}
	if healthResp.Summary.ReauthCount != 1 {
		t.Errorf("expected 1 reauth account, got %d", healthResp.Summary.ReauthCount)
	}

	// Verification of attention-first ordering
	// auth-3 (reauth) should be first, auth-2 (cooldown) second, auth-1 (available) third
	if healthResp.Accounts[0].AuthID != "auth-3" {
		t.Errorf("expected auth-3 (reauth) first, got %s", healthResp.Accounts[0].AuthID)
	}
	if healthResp.Accounts[1].AuthID != "auth-2" {
		t.Errorf("expected auth-2 (cooldown) second, got %s", healthResp.Accounts[1].AuthID)
	}
	if healthResp.Accounts[2].AuthID != "auth-1" {
		t.Errorf("expected auth-1 (healthy) third, got %s", healthResp.Accounts[2].AuthID)
	}

	// 2. GetAccountsQuota
	quotaResp, err := store.GetAccountsQuota(QueryFilter{}, now)
	if err != nil {
		t.Fatalf("GetAccountsQuota failed: %v", err)
	}
	if quotaResp.TotalAccounts != 3 {
		t.Fatalf("expected 3 accounts in quota response, got %d", quotaResp.TotalAccounts)
	}
	// auth-2 is in cooldown, so it should be first in quota list
	if quotaResp.Quotas[0].AuthID != "auth-2" {
		t.Errorf("expected auth-2 first in quota response, got %s", quotaResp.Quotas[0].AuthID)
	}
	if !quotaResp.Quotas[0].InCooldown {
		t.Errorf("expected auth-2 to be in cooldown")
	}
	if quotaResp.Quotas[0].PlanType != "plus" {
		t.Errorf("expected plan_type plus, got %s", quotaResp.Quotas[0].PlanType)
	}
}
