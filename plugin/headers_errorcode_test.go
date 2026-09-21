package plugin

import (
	"net/http"
	"testing"
	"time"
)

// Regression: "X-Should-Retry" was in the ErrorCode header list. It is a retry
// hint whose value is "true"/"false", not an error code, and upstreams send it
// on successful responses too, which polluted diagnostics grouping.
func TestShouldRetryIsNotAnErrorCode(t *testing.T) {
	h := http.Header{}
	h.Set("X-Should-Retry", "true")

	d := ParseResponseHeaders(h, http.StatusOK, time.Now())
	if d.ErrorCode != "" {
		t.Errorf("ErrorCode = %q on a 200 response, want empty", d.ErrorCode)
	}

	// Even on a failure the retry hint must not masquerade as an error code.
	d = ParseResponseHeaders(h, http.StatusTooManyRequests, time.Now())
	if d.ErrorCode == "true" || d.ErrorCode == "false" {
		t.Errorf("ErrorCode = %q, want the retry hint to be excluded", d.ErrorCode)
	}
}

// Real error-code headers must still be captured.
func TestRealErrorCodeHeadersStillCaptured(t *testing.T) {
	for _, key := range []string{"X-Error-Code", "X-Ide-Error-Code", "X-Openai-Ide-Error-Code"} {
		h := http.Header{}
		h.Set(key, "quota_exceeded")
		h.Set("X-Should-Retry", "true")

		d := ParseResponseHeaders(h, http.StatusTooManyRequests, time.Now())
		if d.ErrorCode != "quota_exceeded" {
			t.Errorf("%s: ErrorCode = %q, want quota_exceeded", key, d.ErrorCode)
		}
	}
}
