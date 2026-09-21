package storage

import (
	"testing"
	"time"
)

func TestEvaluateAccountQuotaUsesHeaderWindowMetadata(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	primaryUsed := 40.0
	secondaryUsed := 90.0
	primaryMinutes := 10080.0
	secondaryMinutes := 43200.0
	primaryReset := now.Add(24 * time.Hour).UnixMilli()
	secondaryReset := now.Add(3 * 24 * time.Hour).UnixMilli()

	q := EvaluateAccountQuota(nil, &Record{
		AuthID:                            "acct",
		Provider:                          "codex",
		RequestedAt:                       now,
		HeaderQuotaUsedPercent:            &primaryUsed,
		HeaderQuotaRecoverAtMS:            primaryReset,
		HeaderQuotaWindowMinutes:          &primaryMinutes,
		HeaderSecondaryQuotaUsedPercent:   &secondaryUsed,
		HeaderSecondaryQuotaRecoverAtMS:   secondaryReset,
		HeaderSecondaryQuotaWindowMinutes: &secondaryMinutes,
	}, now)

	if q.PrimaryWindow == nil || q.PrimaryWindow.WindowKind != "weekly" || q.PrimaryWindow.DurationSeconds != 10080*60 {
		t.Fatalf("primary metadata not ported: %#v", q.PrimaryWindow)
	}
	if q.SecondaryWindow == nil || q.SecondaryWindow.WindowKind != "monthly" || q.SecondaryWindow.DurationSeconds != 43200*60 {
		t.Fatalf("secondary metadata not ported: %#v", q.SecondaryWindow)
	}
	if q.SummaryUsedPercent == nil || *q.SummaryUsedPercent != secondaryUsed {
		t.Fatalf("summary used = %v, want %v", q.SummaryUsedPercent, secondaryUsed)
	}
	if q.SummaryRecoverAtMS != secondaryReset || q.CooldownRemainingSeconds != 3*24*60*60 {
		t.Fatalf("summary reset/cooldown = %d/%d", q.SummaryRecoverAtMS, q.CooldownRemainingSeconds)
	}
}

func TestEvaluateAccountQuotaSummaryTieUsesLaterReset(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	used := 50.0
	primaryReset := now.Add(time.Hour).UnixMilli()
	secondaryReset := now.Add(2 * time.Hour).UnixMilli()
	q := EvaluateAccountQuota(nil, &Record{
		HeaderQuotaUsedPercent:          &used,
		HeaderQuotaRecoverAtMS:          primaryReset,
		HeaderSecondaryQuotaUsedPercent: &used,
		HeaderSecondaryQuotaRecoverAtMS: secondaryReset,
	}, now)
	if q.SummaryRecoverAtMS != secondaryReset || q.CooldownRemainingSeconds != 7200 {
		t.Fatalf("tie selection = %d/%d, want %d/7200", q.SummaryRecoverAtMS, q.CooldownRemainingSeconds, secondaryReset)
	}
}

func TestBuildQuotaForecastUsesConservativeBurnRate(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	used := 60.0
	window := &AccountQuotaWindowDetail{
		WindowKind:  "five_hour",
		UsedPercent: &used,
		ResetAtMS:   resetAt,
	}
	forecast := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-3 * time.Hour), Used: 20, ResetAtMS: resetAt, WindowKind: "five_hour"},
		{At: now.Add(-2 * time.Hour), Used: 35, ResetAtMS: resetAt, WindowKind: "five_hour"},
		{At: now.Add(-1 * time.Hour), Used: 50, ResetAtMS: resetAt, WindowKind: "five_hour"},
	}, now)
	if forecast == nil || forecast.Status != "available" {
		t.Fatalf("forecast = %#v, want available", forecast)
	}
	if forecast.BurnRatePercentPerHour != 15 || forecast.EstimatedExhaustionAfterSec != 9600 {
		t.Fatalf("forecast rate/time = %.2f/%d, want 15/9600", forecast.BurnRatePercentPerHour, forecast.EstimatedExhaustionAfterSec)
	}
	if forecast.SampleCount != 3 || forecast.EstimatedExhaustionAtMS != now.Add(160*time.Minute).UnixMilli() {
		t.Fatalf("forecast samples/time = %d/%d", forecast.SampleCount, forecast.EstimatedExhaustionAtMS)
	}
}

func TestBuildQuotaForecastRejectsResetBoundaryAndAfterResetProjection(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour).UnixMilli()
	used := 40.0
	window := &AccountQuotaWindowDetail{WindowKind: "weekly", UsedPercent: &used, ResetAtMS: resetAt}
	withReset := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-3 * time.Hour), Used: 80, ResetAtMS: resetAt, WindowKind: "weekly"},
		{At: now.Add(-2 * time.Hour), Used: 10, ResetAtMS: resetAt, WindowKind: "weekly"},
		{At: now.Add(-1 * time.Hour), Used: 20, ResetAtMS: resetAt, WindowKind: "weekly"},
	}, now)
	if withReset == nil || withReset.SampleCount != 2 || withReset.Status != "after_reset" {
		t.Fatalf("reset-boundary forecast = %#v, want two samples and after_reset", withReset)
	}

	shortReset := now.Add(30 * time.Minute).UnixMilli()
	window.ResetAtMS = shortReset
	afterReset := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-2 * time.Hour), Used: 10, ResetAtMS: shortReset, WindowKind: "weekly"},
		{At: now.Add(-1 * time.Hour), Used: 20, ResetAtMS: shortReset, WindowKind: "weekly"},
	}, now)
	if afterReset == nil || afterReset.Status != "after_reset" || afterReset.EstimatedExhaustionAtMS != 0 {
		t.Fatalf("after-reset forecast = %#v, want unavailable estimate", afterReset)
	}
}

func TestBuildQuotaForecastRequiresUsableHistory(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	used := 40.0
	window := &AccountQuotaWindowDetail{WindowKind: "five_hour", UsedPercent: &used, ResetAtMS: now.Add(time.Hour).UnixMilli()}
	if got := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-30 * time.Second), Used: 20, ResetAtMS: window.ResetAtMS, WindowKind: "five_hour"},
		{At: now, Used: 40, ResetAtMS: window.ResetAtMS, WindowKind: "five_hour"},
	}, now); got != nil {
		t.Fatalf("sub-minute forecast = %#v, want nil", got)
	}

	if got := buildQuotaForecast(window, []quotaObservation{
		{At: now.Add(-2 * time.Hour), Used: 20, ResetAtMS: window.ResetAtMS, WindowKind: "five_hour"},
		{At: now, Used: 120, ResetAtMS: window.ResetAtMS, WindowKind: "five_hour"},
	}, now); got != nil {
		t.Fatalf("out-of-range observation forecast = %#v, want nil", got)
	}
}

func TestEvaluateAccountQuotaReachedFallbackUsesLatestExhaustedReset(t *testing.T) {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	primaryUsed := 100.0
	secondaryUsed := 100.0
	primaryReset := now.Add(time.Hour).UnixMilli()
	secondaryReset := now.Add(3 * time.Hour).UnixMilli()
	q := EvaluateAccountQuota(nil, &Record{
		HeaderQuotaUsedPercent:          &primaryUsed,
		HeaderQuotaRecoverAtMS:          primaryReset,
		HeaderSecondaryQuotaUsedPercent: &secondaryUsed,
		HeaderSecondaryQuotaRecoverAtMS: secondaryReset,
	}, now)
	if q.ReachedWindowKind != "weekly" || q.ReachedWindowSource != "secondary" {
		t.Fatalf("reached window = %s/%s", q.ReachedWindowKind, q.ReachedWindowSource)
	}
	if q.SummaryRecoverAtMS != secondaryReset || q.CooldownRemainingSeconds != 3*60*60 {
		t.Fatalf("reached reset/cooldown = %d/%d", q.SummaryRecoverAtMS, q.CooldownRemainingSeconds)
	}
}
