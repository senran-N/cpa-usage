package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// registerWithConfig resets the singleton's configuration through the normal
// lifecycle call and returns the management registration response.
func registerWithConfig(t *testing.T, configYAML string) managementRegistrationResp {
	t.Helper()

	// Not t.TempDir(): the singleton keeps the first database it opened, and
	// Windows refuses to unlink a file that is still open, which would fail the
	// test during cleanup rather than on an assertion.
	tempDir, err := os.MkdirTemp("", "cpa-usage-routes-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	dbPath := filepath.ToSlash(filepath.Join(tempDir, "routes.db"))
	full := "db_path: \"" + dbPath + "\"\n" + configYAML

	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    []byte(full),
		"schema_version": 6,
	})
	if _, errRegister := Instance().Register(regReq); errRegister != nil {
		t.Fatalf("register failed: %v", errRegister)
	}

	raw, errManagement := Instance().RegisterManagement(nil)
	if errManagement != nil {
		t.Fatalf("RegisterManagement failed: %v", errManagement)
	}
	var resp managementRegistrationResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("failed to unmarshal management registration: %v", err)
	}
	return resp
}

func resourcePaths(resp managementRegistrationResp) []string {
	out := make([]string, 0, len(resp.Resources))
	for _, r := range resp.Resources {
		out = append(out, r.Path)
	}
	return out
}

// The JSON endpoints must not appear on the host's unauthenticated resource
// path unless the operator explicitly opted in.
func TestResourceRoutesExcludeAPIByDefault(t *testing.T) {
	resp := registerWithConfig(t, "retention_days: 30\n")

	paths := resourcePaths(resp)
	if len(paths) != 1 || paths[0] != "/dashboard" {
		t.Fatalf("resource routes = %v, want only /dashboard", paths)
	}

	// The management routes carry the full API routes (including methods for each endpoint).
	if len(resp.Routes) < len(apiEndpoints) {
		t.Fatalf("management routes = %d, want at least %d", len(resp.Routes), len(apiEndpoints))
	}
	foundRoutes := make(map[string]bool)
	for _, r := range resp.Routes {
		foundRoutes[r.Method+" "+r.Path] = true
	}
	if !foundRoutes["POST /usage/cleanup"] {
		t.Errorf("missing POST /usage/cleanup management route")
	}
	if !foundRoutes["GET /usage/accounts/health"] {
		t.Errorf("missing GET /usage/accounts/health management route")
	}
	if !foundRoutes["GET /usage/prices"] {
		t.Errorf("missing GET /usage/prices management route")
	}
}

func TestResourceRoutesIncludeAPIWhenOptedIn(t *testing.T) {
	resp := registerWithConfig(t, "retention_days: 30\nunauthenticated_api: true\n")

	paths := resourcePaths(resp)
	if len(paths) != 1+len(apiEndpoints) {
		t.Fatalf("resource routes = %v, want /dashboard plus %d API paths", paths, len(apiEndpoints))
	}
	found := make(map[string]bool, len(paths))
	for _, p := range paths {
		found[p] = true
	}
	for _, name := range apiEndpoints {
		if !found["/api/"+name] {
			t.Errorf("missing resource route /api/%s", name)
		}
	}

	// Reset the singleton so ordering between tests does not leak the opt-in.
	registerWithConfig(t, "retention_days: 30\n")
}

// Requests that reach the handler on the resource path must be refused while
// the opt-in is off, even if a stale route table still points there.
func TestHandleManagementRejectsResourceAPIWhenDisabled(t *testing.T) {
	registerWithConfig(t, "retention_days: 30\n")

	req, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-usage/api/records",
		Query:  url.Values{},
	})
	raw, err := Instance().HandleManagement(req)
	if err != nil {
		t.Fatalf("HandleManagement failed: %v", err)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	// The same request on the management path is served normally.
	req, _ = json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/usage/records",
		Query:  url.Values{},
	})
	raw, err = Instance().HandleManagement(req)
	if err != nil {
		t.Fatalf("HandleManagement failed: %v", err)
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("management path status = %d, want 200", resp.StatusCode)
	}
}

// The dashboard document must tell the page which mode it is running in.
func TestDashboardCarriesBootConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"default", "retention_days: 30\n", `{"unauthenticated_api":false}`},
		{"opted in", "retention_days: 30\nunauthenticated_api: true\n", `{"unauthenticated_api":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registerWithConfig(t, tc.config)

			req, _ := json.Marshal(pluginapi.ManagementRequest{
				Method: http.MethodGet,
				Path:   "/v0/resource/plugins/cpa-usage/dashboard",
				Query:  url.Values{},
			})
			raw, err := Instance().HandleManagement(req)
			if err != nil {
				t.Fatalf("HandleManagement failed: %v", err)
			}
			var resp pluginapi.ManagementResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if !strings.Contains(string(resp.Body), tc.want) {
				t.Errorf("dashboard document does not contain %s", tc.want)
			}
		})
	}

	registerWithConfig(t, "retention_days: 30\n")
}
