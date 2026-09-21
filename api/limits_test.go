package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"cpa-usage/pricing"
	"cpa-usage/storage"
)

func newLimitsHandler(t *testing.T) *Handler {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(store, pricing.Default())
}

// Regression: handleRecords used to echo the raw requested page_size while the
// storage layer silently clamped it, so clients paginated with a stride that
// skipped rows.
func TestRecordsPageSizeEchoIsClamped(t *testing.T) {
	h := newLimitsHandler(t)

	cases := []struct {
		requested string
		want      int
	}{
		{"", storage.DefaultPageSize},
		{"0", storage.DefaultPageSize},
		{"-5", storage.DefaultPageSize},
		{"37", 37},
		{"5000", storage.MaxPageSize},
	}

	for _, tc := range cases {
		q := url.Values{}
		if tc.requested != "" {
			q.Set("page_size", tc.requested)
		}
		resp := h.Handle(http.MethodGet, "/usage/records", q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page_size=%q: status %d: %s", tc.requested, resp.StatusCode, resp.Body)
		}
		var out struct {
			PageSize int `json:"page_size"`
		}
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("page_size=%q: %v", tc.requested, err)
		}
		if out.PageSize != tc.want {
			t.Errorf("page_size=%q echoed %d, want %d", tc.requested, out.PageSize, tc.want)
		}
	}
}

// Regression: the export limit was unbounded, so a single request could pull
// the whole table into memory while the payload was buffered.
func TestExportLimitIsClamped(t *testing.T) {
	if got := storage.ClampExportLimit(99999999); got != storage.MaxExportLimit {
		t.Errorf("ClampExportLimit(99999999) = %d, want %d", got, storage.MaxExportLimit)
	}
	if got := storage.ClampExportLimit(0); got != storage.DefaultExportLimit {
		t.Errorf("ClampExportLimit(0) = %d, want %d", got, storage.DefaultExportLimit)
	}
	if got := storage.ClampExportLimit(25); got != 25 {
		t.Errorf("ClampExportLimit(25) = %d, want 25", got)
	}

	h := newLimitsHandler(t)
	q := url.Values{}
	q.Set("limit", "99999999")
	q.Set("format", "json")
	resp := h.Handle(http.MethodGet, "/usage/export", q, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, resp.Body)
	}
}

// Regression: imported records bypassed the credential redaction applied on the
// live ingest path, letting an import write secrets back into the database.
func TestImportSanitizesFailureBody(t *testing.T) {
	h := newLimitsHandler(t)

	payload := `{"provider":"openai","model":"gpt-4o","auth_id":"acct-1",` +
		`"requested_at":"2026-01-01T00:00:00Z","failed":true,"failure_status_code":401,` +
		`"failure_body":"{\"error\":\"cpaManagementKey=super-secret-value rejected\"}"}`

	resp := h.Handle(http.MethodPost, "/usage/import", url.Values{}, []byte(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import status %d: %s", resp.StatusCode, resp.Body)
	}

	recResp := h.Handle(http.MethodGet, "/usage/records", url.Values{}, nil)
	if recResp.StatusCode != http.StatusOK {
		t.Fatalf("records status %d: %s", recResp.StatusCode, recResp.Body)
	}

	body := string(recResp.Body)
	if strings.Contains(body, "super-secret-value") {
		t.Errorf("imported failure_body kept the raw secret: %s", body)
	}
	if !strings.Contains(body, "redacted") {
		t.Errorf("expected a redaction marker in stored failure_body: %s", body)
	}
}
