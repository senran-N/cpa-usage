package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-usage-plugin-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "plugin_test.db")
	p := Instance()

	// 1. Test Register
	cfgYAML := []byte("db_path: \"" + filepath.ToSlash(dbPath) + "\"\nretention_days: 30\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfgYAML,
		"schema_version": 6,
	})

	regRespRaw, err := p.Register(regReq)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	var regResp registrationResponse
	if err := json.Unmarshal(regRespRaw, &regResp); err != nil {
		t.Fatalf("failed to unmarshal regResp: %v", err)
	}
	if regResp.Metadata.Name != "cpa-usage" {
		t.Errorf("expected name cpa-usage, got %s", regResp.Metadata.Name)
	}
	if !regResp.Capabilities["usage_plugin"] || !regResp.Capabilities["management_api"] {
		t.Errorf("expected usage_plugin and management_api capabilities, got %+v", regResp.Capabilities)
	}

	// 2. Test HandleUsage
	usageRec := pluginapi.UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4o",
		APIKey:      "sk-test-client",
		AuthID:      "openai-acc-1",
		RequestedAt: time.Now(),
		Latency:     500 * time.Millisecond,
		TTFT:        100 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			InputTokens:     1000,
			OutputTokens:    500,
			CacheReadTokens: 200,
			TotalTokens:     1500,
		},
	}
	usageRaw, _ := json.Marshal(usageRec)
	if _, err := p.HandleUsage(usageRaw); err != nil {
		t.Fatalf("handle usage failed: %v", err)
	}

	// Allow flush
	time.Sleep(300 * time.Millisecond)

	// 3. Test RegisterManagement
	mgmtRegRaw, err := p.RegisterManagement(nil)
	if err != nil {
		t.Fatalf("register management failed: %v", err)
	}
	var mgmtReg managementRegistrationResp
	if err := json.Unmarshal(mgmtRegRaw, &mgmtReg); err != nil {
		t.Fatalf("failed to parse mgmt reg: %v", err)
	}
	if len(mgmtReg.Routes) == 0 || len(mgmtReg.Resources) == 0 {
		t.Errorf("expected routes and resources in management registration")
	}

	// 4. Test HandleManagement for HTML Dashboard
	reqHTML := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/dashboard",
	}
	reqHTMLRaw, _ := json.Marshal(reqHTML)
	respHTMLRaw, err := p.HandleManagement(reqHTMLRaw)
	if err != nil {
		t.Fatalf("handle management html failed: %v", err)
	}
	var respHTML pluginapi.ManagementResponse
	if err := json.Unmarshal(respHTMLRaw, &respHTML); err != nil {
		t.Fatalf("failed to parse mgmt response html: %v", err)
	}
	if respHTML.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", respHTML.StatusCode)
	}
	if len(respHTML.Body) == 0 {
		t.Errorf("expected non-empty html body")
	}

	// 5. Test HandleManagement for JSON API (/api/summary)
	reqAPI := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/api/summary",
		Query:  url.Values{},
	}
	reqAPIRaw, _ := json.Marshal(reqAPI)
	respAPIRaw, err := p.HandleManagement(reqAPIRaw)
	if err != nil {
		t.Fatalf("handle management api failed: %v", err)
	}
	var respAPI pluginapi.ManagementResponse
	if err := json.Unmarshal(respAPIRaw, &respAPI); err != nil {
		t.Fatalf("failed to parse mgmt response api: %v", err)
	}
	if respAPI.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for api summary, got %d", respAPI.StatusCode)
	}
	if len(respAPI.Body) == 0 {
		t.Errorf("expected non-empty summary json body")
	}

	// 6. Test Shutdown
	p.Shutdown()
}
