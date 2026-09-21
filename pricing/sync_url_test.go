package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression: SyncPrices fetched a caller-supplied URL with no scheme check, so
// the server could be pointed at non-HTTP schemes and made to read local
// resources on the caller's behalf.
func TestSyncPricesRejectsNonHTTPSchemes(t *testing.T) {
	e := Default()

	cases := []string{
		"file:///etc/passwd",
		"file://C:/Windows/win.ini",
		"gopher://127.0.0.1:11211/_stats",
		"ftp://example.com/prices.json",
		"data:application/json,{}",
	}

	for _, raw := range cases {
		_, _, err := e.SyncPrices(context.Background(), "litellm", raw)
		if err == nil {
			t.Errorf("SyncPrices(%q) succeeded, want a scheme rejection", raw)
			continue
		}
		if !strings.Contains(err.Error(), "unsupported price sync URL scheme") &&
			!strings.Contains(err.Error(), "missing a host") {
			t.Errorf("SyncPrices(%q) failed with %v, want a scheme/host validation error", raw, err)
		}
	}
}

// A normal http(s) source must still sync successfully.
func TestSyncPricesAcceptsHTTPSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"test-model": {
				"input_cost_per_token": 0.000001,
				"output_cost_per_token": 0.000002,
				"litellm_provider": "openai",
				"mode": "chat"
			}
		}`))
	}))
	defer srv.Close()

	e := Default()
	res, prices, err := e.SyncPrices(context.Background(), "litellm", srv.URL)
	if err != nil {
		t.Fatalf("SyncPrices over http failed: %v", err)
	}
	if res.ImportedCount != 1 {
		t.Errorf("ImportedCount = %d, want 1", res.ImportedCount)
	}
	if _, ok := prices["test-model"]; !ok {
		t.Errorf("expected test-model in synced prices, got %v", prices)
	}
}
