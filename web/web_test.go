package web

import (
	"bytes"
	"testing"
)

func TestDashboardRendering(t *testing.T) {
	// 1. Default config
	docDefault := Dashboard(BootConfig{UnauthenticatedAPI: false})
	if len(docDefault) == 0 {
		t.Fatalf("rendered dashboard should not be empty")
	}
	if !bytes.Contains(docDefault, []byte(`{"unauthenticated_api":false}`)) {
		t.Errorf("expected default dashboard to contain unauthenticated_api: false")
	}

	// 2. Opted-in config
	docOptedIn := Dashboard(BootConfig{UnauthenticatedAPI: true})
	if !bytes.Contains(docOptedIn, []byte(`{"unauthenticated_api":true}`)) {
		t.Errorf("expected opted-in dashboard to contain unauthenticated_api: true")
	}

	// Verify all new UI feature containers exist in the document
	requiredElements := []string{
		"btnOpenPricesModal",
		"btnOpenDiagnosticsModal",
		"btnOpenDataHubModal",
		"btnOpenCleanupModal",
		"authHealthSummaryBanner",
		"authsTableBody",
		"formatQuotaForecast",
		"estimated_exhaustion_after_seconds",
		"pricesModal",
		"syncPricesPanel",
		"addPriceFormPanel",
		"pricesTableBody",
		"diagnosticsModal",
		"diagFailuresTableBody",
		"dataHubModal",
		"importDropZone",
		"importFileInput",
		"detailModal",
		"modalTelemetryGrid",
		"btnCopyErrorBody",
		"tab-integrity",
		"integrity-total-requests",
		"integrity-match-rate",
		"integrity-mismatch-count",
		"integrityPatternsContainer",
		"integrityTableBody",
		"modalIntegrityBanner",
		"diagModelMismatches",
		"dropdownLang",
	}

	for _, elemID := range requiredElements {
		if !bytes.Contains(docDefault, []byte(elemID)) {
			t.Errorf("missing essential UI element ID %q in dashboard HTML", elemID)
		}
	}
}

func TestDashboardCache(t *testing.T) {
	cfg := BootConfig{UnauthenticatedAPI: false}
	doc1 := Dashboard(cfg)
	doc2 := Dashboard(cfg)

	// Since it's cached in memory, the slice pointer/backing should be reused
	if string(doc1) != string(doc2) {
		t.Errorf("cached doc does not match fresh doc")
	}
}
