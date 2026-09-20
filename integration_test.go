//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type cBuffer struct {
	ptr unsafe.Pointer
	len uintptr
}

type cHostAPI struct {
	abiVersion uint32
	hostCtx    uintptr
	call       uintptr
	freeBuffer uintptr
}

type cPluginAPI struct {
	abiVersion uint32
	call       uintptr
	freeBuffer uintptr
	shutdown   uintptr
}

func TestDLLIntegration(t *testing.T) {
	dllPath, err := filepath.Abs("cpa_usage.dll")
	if err != nil {
		t.Fatalf("failed to resolve dll path: %v", err)
	}
	if _, err := os.Stat(dllPath); err != nil {
		t.Skip("cpa_usage.dll not found, run build first")
	}

	h, err := syscall.LoadLibrary(dllPath)
	if err != nil {
		t.Fatalf("failed to load library: %v", err)
	}
	defer syscall.FreeLibrary(h)

	initProc, err := syscall.GetProcAddress(h, "cliproxy_plugin_init")
	if err != nil {
		t.Fatalf("failed to get cliproxy_plugin_init: %v", err)
	}

	callProc, err := syscall.GetProcAddress(h, "cliproxyPluginCall")
	if err != nil {
		t.Fatalf("failed to get cliproxyPluginCall: %v", err)
	}

	freeProc, err := syscall.GetProcAddress(h, "cliproxyPluginFree")
	if err != nil {
		t.Fatalf("failed to get cliproxyPluginFree: %v", err)
	}

	var hostAPI cHostAPI
	var pluginAPI cPluginAPI

	// 1. Initialize plugin
	r1, _, _ := syscall.SyscallN(initProc, uintptr(unsafe.Pointer(&hostAPI)), uintptr(unsafe.Pointer(&pluginAPI)))
	if r1 != 0 {
		t.Fatalf("cliproxy_plugin_init returned %d", r1)
	}
	if pluginAPI.call == 0 {
		t.Fatalf("pluginAPI.call is nil")
	}

	invoke := func(method string, payload []byte) (bool, []byte, error) {
		cMethod, _ := syscall.BytePtrFromString(method)
		var reqPtr uintptr
		if len(payload) > 0 {
			reqPtr = uintptr(unsafe.Pointer(&payload[0]))
		}
		var respBuf cBuffer

		ret, _, _ := syscall.SyscallN(
			callProc,
			uintptr(unsafe.Pointer(cMethod)),
			reqPtr,
			uintptr(len(payload)),
			uintptr(unsafe.Pointer(&respBuf)),
		)

		var respBytes []byte
		if respBuf.ptr != nil && respBuf.len > 0 {
			respBytes = make([]byte, respBuf.len)
			copy(respBytes, unsafe.Slice((*byte)(respBuf.ptr), respBuf.len))
			syscall.SyscallN(freeProc, uintptr(respBuf.ptr), respBuf.len)
		}

		if ret != 0 {
			return false, respBytes, nil
		}

		var env struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respBytes, &env); err != nil {
			return false, nil, err
		}
		if !env.OK {
			return false, nil, nil
		}
		return true, env.Result, nil
	}

	// 2. Test plugin.register
	ok, res, err := invoke("plugin.register", []byte(`{"schema_version":6,"config_yaml":"db_path: data/test_integration.db"}`))
	if err != nil || !ok {
		t.Fatalf("plugin.register failed: ok=%v, err=%v", ok, err)
	}
	var regResp struct {
		SchemaVersion uint32 `json:"schema_version"`
		Metadata      struct {
			Name string `json:"Name"`
		} `json:"metadata"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &regResp); err != nil {
		t.Fatalf("failed to unmarshal register result: %v", err)
	}
	if regResp.Metadata.Name != "cpa-usage" {
		t.Errorf("expected plugin name cpa-usage, got %s", regResp.Metadata.Name)
	}
	if !regResp.Capabilities["usage_plugin"] || !regResp.Capabilities["management_api"] {
		t.Errorf("expected usage_plugin and management_api in capabilities")
	}

	// 3. Test usage.handle
	mockUsage := map[string]any{
		"Provider":    "openai",
		"Model":       "gpt-4o",
		"APIKey":      "sk-integration-test",
		"AuthID":      "auth-cred-test",
		"RequestedAt": time.Now().Format(time.RFC3339),
		"Latency":     int64(450 * time.Millisecond),
		"Detail": map[string]any{
			"InputTokens":     1200,
			"OutputTokens":    400,
			"CacheReadTokens": 300,
			"TotalTokens":     1600,
		},
	}
	mockUsageRaw, _ := json.Marshal(mockUsage)
	ok, _, err = invoke("usage.handle", mockUsageRaw)
	if err != nil || !ok {
		t.Fatalf("usage.handle failed: ok=%v, err=%v", ok, err)
	}

	// Wait for queue flush
	time.Sleep(350 * time.Millisecond)

	// 4. Test management.register
	ok, res, err = invoke("management.register", nil)
	if err != nil || !ok {
		t.Fatalf("management.register failed: ok=%v, err=%v", ok, err)
	}
	var mgmtReg struct {
		Routes    []map[string]any `json:"routes"`
		Resources []map[string]any `json:"resources"`
	}
	if err := json.Unmarshal(res, &mgmtReg); err != nil {
		t.Fatalf("failed to unmarshal management register: %v", err)
	}
	if len(mgmtReg.Routes) == 0 || len(mgmtReg.Resources) == 0 {
		t.Errorf("expected routes and resources in management.register")
	}

	// 5. Test management.handle for /dashboard
	reqDashboard := map[string]any{
		"Method": "GET",
		"Path":   "/dashboard",
	}
	reqDashboardRaw, _ := json.Marshal(reqDashboard)
	ok, res, err = invoke("management.handle", reqDashboardRaw)
	if err != nil || !ok {
		t.Fatalf("management.handle /dashboard failed: ok=%v, err=%v", ok, err)
	}
	var respDashboard struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	if err := json.Unmarshal(res, &respDashboard); err != nil {
		t.Fatalf("failed to unmarshal dashboard response: %v", err)
	}
	if respDashboard.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", respDashboard.StatusCode)
	}
	if len(respDashboard.Body) == 0 {
		t.Errorf("expected non-empty dashboard body")
	}

	// 6. Test management.handle for /api/summary
	reqSummary := map[string]any{
		"Method": "GET",
		"Path":   "/api/summary",
	}
	reqSummaryRaw, _ := json.Marshal(reqSummary)
	ok, res, err = invoke("management.handle", reqSummaryRaw)
	if err != nil || !ok {
		t.Fatalf("management.handle /api/summary failed: ok=%v, err=%v", ok, err)
	}
	var respSummary struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	if err := json.Unmarshal(res, &respSummary); err != nil {
		t.Fatalf("failed to unmarshal summary response: %v", err)
	}
	if respSummary.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", respSummary.StatusCode)
	}
	var summaryData map[string]any
	if err := json.Unmarshal(respSummary.Body, &summaryData); err != nil {
		t.Fatalf("failed to parse summary data: %v", err)
	}
	if summaryData["total_requests"].(float64) < 1 {
		t.Errorf("expected at least 1 request recorded in summary, got %v", summaryData["total_requests"])
	}

	// Clean up temp test database
	_ = os.RemoveAll("data/test_integration.db")
	_ = os.RemoveAll("data/test_integration.db-shm")
	_ = os.RemoveAll("data/test_integration.db-wal")
}
