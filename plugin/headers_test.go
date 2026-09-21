package plugin

import (
	"net/http"
	"testing"
	"time"
)

func TestParseResponseHeaders(t *testing.T) {
	base := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)

	headers := http.Header{}
	headers.Set("Retry-After", "30")
	headers.Set("X-Request-Id", "req-xyz-123")
	headers.Set("X-Quota-Used-Percent", "85.5")
	headers.Set("Openai-Model", "gpt-4o-2024-08-06")

	derived := ParseResponseHeaders(headers, 429, base)

	if derived.ResponseModel != "gpt-4o-2024-08-06" {
		t.Errorf("expected response_model gpt-4o-2024-08-06, got %s", derived.ResponseModel)
	}
	if derived.ErrorKind != "rate_limit" {
		t.Errorf("expected error_kind rate_limit, got %s", derived.ErrorKind)
	}
	if derived.TraceID != "req-xyz-123" {
		t.Errorf("expected trace_id req-xyz-123, got %s", derived.TraceID)
	}
	expectedRecoverMS := base.Add(30 * time.Second).UnixMilli()
	if derived.QuotaRecoverAtMS != expectedRecoverMS {
		t.Errorf("expected recover ms %d, got %d", expectedRecoverMS, derived.QuotaRecoverAtMS)
	}
	if derived.QuotaUsedPercent == nil || *derived.QuotaUsedPercent != 85.5 {
		t.Errorf("expected quota used 85.5, got %v", derived.QuotaUsedPercent)
	}

	// Test Codex specific primary and secondary headers
	codexHeaders := http.Header{}
	codexHeaders.Set("X-Codex-Plan-Type", "plus")
	codexHeaders.Set("X-Codex-Primary-Used-Percent", "92.5")
	codexHeaders.Set("X-Codex-Primary-Reset-After-Seconds", "7200")
	codexHeaders.Set("X-Codex-Primary-Window-Minutes", "300")
	codexHeaders.Set("X-Codex-Secondary-Used-Percent", "45.0")
	codexHeaders.Set("X-Codex-Secondary-Reset-After-Seconds", "86400")
	codexHeaders.Set("X-Codex-Secondary-Window-Minutes", "10080")
	codexHeaders.Set("X-Codex-Rate-Limit-Reached-Type", "primary")

	codexDerived := ParseResponseHeaders(codexHeaders, 429, base)
	if codexDerived.PlanType != "plus" {
		t.Errorf("expected plan_type plus, got %s", codexDerived.PlanType)
	}
	if codexDerived.QuotaUsedPercent == nil || *codexDerived.QuotaUsedPercent != 92.5 {
		t.Errorf("expected primary used percent 92.5, got %v", codexDerived.QuotaUsedPercent)
	}
	if codexDerived.QuotaWindowMinutes == nil || *codexDerived.QuotaWindowMinutes != 300 {
		t.Errorf("expected primary window minutes 300, got %v", codexDerived.QuotaWindowMinutes)
	}
	if codexDerived.RateLimitReachedType != "primary" {
		t.Errorf("expected reached type primary, got %q", codexDerived.RateLimitReachedType)
	}
	expectedPrimaryRecover := base.Add(7200 * time.Second).UnixMilli()
	if codexDerived.QuotaRecoverAtMS != expectedPrimaryRecover {
		t.Errorf("expected quota recover ms %d, got %d", expectedPrimaryRecover, codexDerived.QuotaRecoverAtMS)
	}
	if codexDerived.SecondaryQuotaUsedPercent == nil || *codexDerived.SecondaryQuotaUsedPercent != 45.0 {
		t.Errorf("expected secondary used percent 45.0, got %v", codexDerived.SecondaryQuotaUsedPercent)
	}
	if codexDerived.SecondaryQuotaWindowMinutes == nil || *codexDerived.SecondaryQuotaWindowMinutes != 10080 {
		t.Errorf("expected secondary window minutes 10080, got %v", codexDerived.SecondaryQuotaWindowMinutes)
	}
	expectedSecondaryRecover := base.Add(86400 * time.Second).UnixMilli()
	if codexDerived.SecondaryQuotaRecoverAtMS != expectedSecondaryRecover {
		t.Errorf("expected secondary recover ms %d, got %d", expectedSecondaryRecover, codexDerived.SecondaryQuotaRecoverAtMS)
	}

	// Test Authorization error header
	authHeaders := http.Header{}
	authHeaders.Set("X-Openai-Authorization-Error", "invalid_api_key")
	authDerived := ParseResponseHeaders(authHeaders, 401, base)
	if authDerived.ErrorKind != "authentication" {
		t.Errorf("expected authentication error_kind, got %s", authDerived.ErrorKind)
	}
	if authDerived.ErrorCode != "invalid_api_key" {
		t.Errorf("expected invalid_api_key error_code, got %s", authDerived.ErrorCode)
	}
}
