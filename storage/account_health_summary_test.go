package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func newHealthTestStorage(t *testing.T) *Storage {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Regression: HealthStatusLowQuota was missing from the summary switch, so
// accounts running low on quota fell through to default and were reported as
// "unknown" instead of being surfaced.
func TestAccountHealthSummaryCountsLowQuota(t *testing.T) {
	store := newHealthTestStorage(t)
	now := time.Now().UTC()

	pct := 90.0
	if err := store.InsertRecord(&Record{
		Provider: "openai", Model: "gpt-4o", AuthID: "acct-low",
		RequestedAt: now, HeaderQuotaUsedPercent: &pct,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := store.GetAccountsHealth(QueryFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(resp.Accounts))
	}
	if resp.Accounts[0].Status != HealthStatusLowQuota {
		t.Fatalf("status = %q, want %q", resp.Accounts[0].Status, HealthStatusLowQuota)
	}
	if resp.Summary.LowQuotaCount != 1 {
		t.Errorf("LowQuotaCount = %d, want 1", resp.Summary.LowQuotaCount)
	}
	if resp.Summary.UnknownCount != 0 {
		t.Errorf("UnknownCount = %d, want 0 (low_quota must not fall through)", resp.Summary.UnknownCount)
	}
	if resp.Summary.TotalAccounts != 1 {
		t.Errorf("TotalAccounts = %d, want 1", resp.Summary.TotalAccounts)
	}
}

// The per-status counters must always add up to the account total, so no
// status can be silently dropped from the summary again.
func TestAccountHealthSummaryCountsAreExhaustive(t *testing.T) {
	store := newHealthTestStorage(t)
	now := time.Now().UTC()

	lowPct := 90.0
	fullPct := 100.0
	records := []*Record{
		{Provider: "openai", Model: "gpt-4o", AuthID: "acct-ok", RequestedAt: now},
		{Provider: "openai", Model: "gpt-4o", AuthID: "acct-low", RequestedAt: now, HeaderQuotaUsedPercent: &lowPct},
		{Provider: "openai", Model: "gpt-4o", AuthID: "acct-full", RequestedAt: now, HeaderQuotaUsedPercent: &fullPct},
		{
			Provider: "openai", Model: "gpt-4o", AuthID: "acct-reauth", RequestedAt: now,
			Failed: true, FailureStatusCode: 401, HeaderErrorKind: "authentication",
		},
	}
	for _, r := range records {
		if err := store.InsertRecord(r); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := store.GetAccountsHealth(QueryFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}

	s := resp.Summary
	sum := s.HealthyCount + s.LowQuotaCount + s.CooldownCount +
		s.ExhaustedCount + s.ReauthCount + s.ErrorCount + s.UnknownCount
	if sum != s.TotalAccounts {
		t.Errorf("per-status counts sum to %d but TotalAccounts = %d (summary = %+v)", sum, s.TotalAccounts, s)
	}
	if s.TotalAccounts != len(records) {
		t.Errorf("TotalAccounts = %d, want %d", s.TotalAccounts, len(records))
	}
}

// Page size and export limits are enforced in storage; handlers reuse these
// helpers so what they report matches what the query actually uses.
func TestClampPageSizeAndExportLimit(t *testing.T) {
	if got := ClampPageSize(0); got != DefaultPageSize {
		t.Errorf("ClampPageSize(0) = %d, want %d", got, DefaultPageSize)
	}
	if got := ClampPageSize(-1); got != DefaultPageSize {
		t.Errorf("ClampPageSize(-1) = %d, want %d", got, DefaultPageSize)
	}
	if got := ClampPageSize(50); got != 50 {
		t.Errorf("ClampPageSize(50) = %d, want 50", got)
	}
	if got := ClampPageSize(MaxPageSize + 1); got != MaxPageSize {
		t.Errorf("ClampPageSize(over) = %d, want %d", got, MaxPageSize)
	}
	if got := ClampExportLimit(MaxExportLimit + 1); got != MaxExportLimit {
		t.Errorf("ClampExportLimit(over) = %d, want %d", got, MaxExportLimit)
	}
}
