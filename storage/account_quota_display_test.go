package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A quota window always resets at some future moment, so "reset in the future"
// is not evidence of a rate limit. Regression test: an account that is
// successfully serving requests used to be rendered as "冷却 (19m)" purely
// because its 5H window had not reset yet.
func TestFutureWindowResetDoesNotPutHealthyAccountOnCooldown(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()
	used := 71.0
	minutes := 300.0

	stat := &AuthStat{AuthID: "acc-in-use", Provider: "codex", Requests: 40, FailedCount: 0}
	rec := &Record{
		AuthID:                   "acc-in-use",
		Provider:                 "codex",
		RequestedAt:              now.Add(-20 * time.Second),
		HeaderQuotaUsedPercent:   &used,
		HeaderQuotaRecoverAtMS:   nowMS + 19*60*1000, // window resets in 19 minutes
		HeaderQuotaWindowMinutes: &minutes,
	}

	h := EvaluateAccountHealth(stat, rec, now)
	if h.InCooldown {
		t.Fatalf("window reset mistaken for a cooldown: %#v", h)
	}
	if h.RateLimited {
		t.Fatalf("no rate-limit evidence on record, got rate_limited=true")
	}
	if h.CooldownRemainingSeconds != 0 {
		t.Fatalf("cooldown remaining = %d, want 0", h.CooldownRemainingSeconds)
	}
	if h.ResetRemainingSeconds != 19*60 {
		t.Fatalf("reset countdown = %d, want %d", h.ResetRemainingSeconds, 19*60)
	}
	if h.Status != HealthStatusAvailable {
		t.Fatalf("status = %s (%s), want available", h.Status, h.StatusReason)
	}
}

// Real rate limiting must still be reported as a cooldown with a countdown.
func TestRateLimitEvidenceIsStillCoolingDown(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()
	used := 95.0

	stat := &AuthStat{AuthID: "acc-limited", Provider: "codex", Requests: 20, FailedCount: 2}
	rec := &Record{
		AuthID:                 "acc-limited",
		Provider:               "codex",
		RequestedAt:            now.Add(-30 * time.Second),
		Failed:                 true,
		FailureStatusCode:      429,
		HeaderErrorKind:        "rate_limit",
		HeaderQuotaUsedPercent: &used,
		HeaderQuotaRecoverAtMS: nowMS + 30*60*1000,
	}
	if h := EvaluateAccountHealth(stat, rec, now); !h.InCooldown || h.CooldownRemainingSeconds != 30*60 {
		t.Fatalf("429 with a reset time should cool down: in_cooldown=%v remaining=%d", h.InCooldown, h.CooldownRemainingSeconds)
	}

	// Reached-window markers count as rate limiting even without a 429.
	reached := &Record{
		AuthID:                 "acc-reached",
		Provider:               "codex",
		RequestedAt:            now.Add(-30 * time.Second),
		RateLimitReachedType:   "primary",
		HeaderQuotaUsedPercent: &used,
		HeaderQuotaRecoverAtMS: nowMS + 10*60*1000,
	}
	if h := EvaluateAccountHealth(stat, reached, now); !h.InCooldown || !h.RateLimited {
		t.Fatalf("reached-window marker should cool down: %#v", h)
	}
}

// End to end: an account that hit a 429 earlier but is succeeding again must not
// stay pinned to "冷却中".
func TestAccountRecoveringFromRateLimitIsAvailableAgain(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-recover-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := Open(filepath.Join(tempDir, "recover.db"))
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	minutes := 300.0
	usedThen := 70.0
	usedNow := 71.0
	resetAt := now.Add(19 * time.Minute).UnixMilli()

	records := []*Record{
		{
			AuthID:                   "acc-busy",
			Provider:                 "codex",
			Model:                    "gpt-5-codex",
			RequestedAt:              now.Add(-5 * time.Minute),
			Failed:                   true,
			FailureStatusCode:        429,
			HeaderErrorKind:          "rate_limit",
			HeaderQuotaUsedPercent:   &usedThen,
			HeaderQuotaRecoverAtMS:   resetAt,
			HeaderQuotaWindowMinutes: &minutes,
		},
		{
			AuthID:                   "acc-busy",
			Provider:                 "codex",
			Model:                    "gpt-5-codex",
			RequestedAt:              now.Add(-20 * time.Second),
			HeaderQuotaUsedPercent:   &usedNow,
			HeaderQuotaRecoverAtMS:   resetAt,
			HeaderQuotaWindowMinutes: &minutes,
		},
	}
	if err := store.BatchInsertRecords(records); err != nil {
		t.Fatalf("failed to insert records: %v", err)
	}

	resp, err := store.GetAccountsHealth(QueryFilter{}, now)
	if err != nil {
		t.Fatalf("GetAccountsHealth failed: %v", err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("accounts = %#v", resp.Accounts)
	}
	got := resp.Accounts[0]
	if got.InCooldown || got.Status != HealthStatusAvailable {
		t.Fatalf("account still flagged while in use: status=%s in_cooldown=%v reason=%q",
			got.Status, got.InCooldown, got.StatusReason)
	}
	if resp.Summary.CooldownCount != 0 {
		t.Fatalf("summary cooldown count = %d, want 0", resp.Summary.CooldownCount)
	}
	if got.ResetRemainingSeconds != 19*60 {
		t.Fatalf("reset countdown = %d, want %d", got.ResetRemainingSeconds, 19*60)
	}
}

// Upstream reports used percent as coarse integer steps while sampling several
// times a minute. The old estimator only looked at neighbouring samples spaced
// at least a minute apart, so it almost never found a usable rate and the
// dashboard showed "暂无足够历史" instead of a prediction.
func TestQuotaForecastSurvivesIntegerQuantisedSamples(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	lastUsed := 60.0
	window := &AccountQuotaWindowDetail{
		WindowKind:  "five_hour",
		UsedPercent: &lastUsed,
		ResetAtMS:   resetAt,
	}

	// 20 samples 30 seconds apart, percentage stepping up one integer every two
	// samples. No neighbouring pair is spaced a full minute, so the previous
	// estimator produced nothing at all.
	observations := make([]quotaObservation, 0, 20)
	for i := 0; i < 20; i++ {
		observations = append(observations, quotaObservation{
			At:         now.Add(time.Duration(i-19) * 30 * time.Second),
			Used:       60 + float64(i/2),
			ResetAtMS:  resetAt,
			WindowKind: "five_hour",
		})
	}

	forecast := buildQuotaForecast(window, observations, now)
	if forecast == nil {
		t.Fatalf("forecast = nil, want a prediction from quantised samples")
	}
	if forecast.Status != "available" {
		t.Fatalf("forecast status = %s, want available", forecast.Status)
	}
	if forecast.SampleCount != 20 {
		t.Fatalf("sample count = %d, want 20", forecast.SampleCount)
	}
	// (69 - 60) percent over 570 seconds.
	wantRate := 9.0 * 3600.0 / 570.0
	if math.Abs(forecast.BurnRatePercentPerHour-wantRate) > 0.001 {
		t.Fatalf("burn rate = %.4f, want %.4f", forecast.BurnRatePercentPerHour, wantRate)
	}
}

// A window whose usage is flat has nothing to extrapolate; say so instead of
// showing nothing.
func TestQuotaForecastReportsStableWhenUsageIsFlat(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	used := 50.0
	window := &AccountQuotaWindowDetail{WindowKind: "five_hour", UsedPercent: &used, ResetAtMS: resetAt}

	forecast := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-2 * time.Hour), Used: 50, ResetAtMS: resetAt, WindowKind: "five_hour"},
		{At: now.Add(-time.Hour), Used: 50, ResetAtMS: resetAt, WindowKind: "five_hour"},
		{At: now, Used: 50, ResetAtMS: resetAt, WindowKind: "five_hour"},
	}, now)
	if forecast == nil || forecast.Status != "stable" {
		t.Fatalf("flat forecast = %#v, want stable", forecast)
	}
}

// The 5H and weekly columns each need their own estimated allowance. Upstream
// only reports a percentage, so it is back-calculated from what this proxy
// recorded inside the window.
func TestQuotaWindowEstimatesRemainingAllowancePerWindow(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	used := 25.0
	window := &AccountQuotaWindowDetail{WindowKind: "five_hour", UsedPercent: &used, ResetAtMS: resetAt}

	observations := []quotaObservation{
		{At: now.Add(-10 * time.Minute), Used: 20, ResetAtMS: resetAt, WindowKind: "five_hour", TotalTokens: 30000},
		{At: now.Add(-5 * time.Minute), Used: 25, ResetAtMS: resetAt, WindowKind: "five_hour", TotalTokens: 20000},
		// Belongs to a previous window and must not count.
		{At: now.Add(-20 * time.Minute), Used: 5, ResetAtMS: resetAt - 5*60*60*1000, WindowKind: "five_hour", TotalTokens: 999999},
	}
	applyWindowEstimates(window, observations, now)

	if window.ConsumedTokens != 50000 || window.ConsumedRequests != 2 {
		t.Fatalf("consumption = %d tokens / %d requests, want 50000/2", window.ConsumedTokens, window.ConsumedRequests)
	}
	if window.EstimatedTotalTokens == nil || *window.EstimatedTotalTokens != 200000 {
		t.Fatalf("estimated total = %v, want 200000 (50000 at 25%%)", window.EstimatedTotalTokens)
	}
	if window.EstimatedRemainingTokens == nil || *window.EstimatedRemainingTokens != 150000 {
		t.Fatalf("estimated remaining = %v, want 150000", window.EstimatedRemainingTokens)
	}
	if window.EstimatedRemainingRequests == nil || *window.EstimatedRemainingRequests != 6 {
		t.Fatalf("estimated remaining requests = %v, want 6", window.EstimatedRemainingRequests)
	}

	// A barely-moved window must not publish an inflated absolute estimate.
	barelyUsed := 0.5
	tiny := &AccountQuotaWindowDetail{WindowKind: "weekly", UsedPercent: &barelyUsed, ResetAtMS: resetAt}
	applyWindowEstimates(tiny, []quotaObservation{
		{At: now.Add(-time.Minute), Used: 0.5, ResetAtMS: resetAt, WindowKind: "weekly", TotalTokens: 100},
	}, now)
	if tiny.EstimatedTotalTokens != nil || tiny.EstimatedRemainingTokens != nil {
		t.Fatalf("estimate published for a 0.5%% window: %#v", tiny)
	}
	if tiny.ConsumedTokens != 100 || tiny.ConsumedRequests != 1 {
		t.Fatalf("consumption should still be reported: %d/%d", tiny.ConsumedTokens, tiny.ConsumedRequests)
	}
}
