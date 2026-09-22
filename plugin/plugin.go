package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-usage/api"
	"cpa-usage/pricing"
	"cpa-usage/sanitize"
	"cpa-usage/storage"
	"cpa-usage/web"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// Config represents plugin-specific configuration settings.
type Config struct {
	DBPath        string `yaml:"db_path"`
	RetentionDays int    `yaml:"retention_days"`
	// UnauthenticatedAPI exposes the JSON endpoints under the host's resource
	// path, which CPA serves without management authentication. It defaults to
	// false: the dashboard then talks to the authenticated management routes
	// instead. Only enable it when CPA is reachable from trusted hosts alone.
	UnauthenticatedAPI bool `yaml:"unauthenticated_api"`
}

// Plugin manages the usage collector and management dashboard lifecycle.
type Plugin struct {
	mu         sync.RWMutex
	config     Config
	store      *storage.Storage
	pricing    *pricing.Engine
	apiHandler *api.Handler
}

var (
	globalPlugin *Plugin
	pluginOnce   sync.Once
)

// Instance returns the singleton Plugin instance.
func Instance() *Plugin {
	pluginOnce.Do(func() {
		globalPlugin = &Plugin{
			config: Config{
				DBPath:        "data/cpa_usage.db",
				RetentionDays: 90,
			},
			pricing: pricing.Default(),
		}
	})
	return globalPlugin
}

type registrationResponse struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  map[string]bool    `json:"capabilities"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

// Register processes plugin.register lifecycle calls.
func (p *Plugin) Register(payload []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var req lifecycleRequest
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &req)
	}

	if len(req.ConfigYAML) > 0 {
		var cfg Config
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err == nil {
			if cfg.DBPath != "" {
				p.config.DBPath = cfg.DBPath
			}
			if cfg.RetentionDays > 0 {
				p.config.RetentionDays = cfg.RetentionDays
			}
			// Absent key leaves this false, which is the safe default.
			p.config.UnauthenticatedAPI = cfg.UnauthenticatedAPI
		}
	}

	// Initialize storage if not already opened
	if p.store == nil {
		if err := p.initStorageLocked(); err != nil {
			return nil, fmt.Errorf("failed to init storage: %w", err)
		}
	}

	resp := registrationResponse{
		SchemaVersion: 6,
		Metadata: pluginapi.Metadata{
			Name:             "cpa-usage",
			Version:          "1.0.0",
			Author:           "senran-N",
			GitHubRepository: "https://github.com/senran-N/cpa-usage",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "db_path",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Path to SQLite database file (default: data/cpa_usage.db)",
				},
				{
					Name:        "retention_days",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Retention period in days for usage data (0 for unlimited, default: 90)",
				},
				{
					Name:        "unauthenticated_api",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Expose the JSON endpoints on the unauthenticated resource path (default: false). Leave disabled unless CPA is reachable only from trusted hosts.",
				},
			},
		},
		Capabilities: map[string]bool{
			"usage_plugin":   true,
			"management_api": true,
		},
	}

	return json.Marshal(resp)
}

// Reconfigure updates configuration on reload.
func (p *Plugin) Reconfigure(payload []byte) ([]byte, error) {
	return p.Register(payload)
}

func (p *Plugin) initStorageLocked() error {
	dbPath := p.config.DBPath
	if dbPath == "" {
		dbPath = "data/cpa_usage.db"
	}
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	p.store = store
	p.apiHandler = api.NewHandler(store, p.pricing)

	// Load previously synced and custom prices from persistent storage
	if synced, err := store.LoadSyncedPrices(); err == nil && len(synced) > 0 {
		syncedMap := make(map[string]*pricing.ModelPricing, len(synced))
		for _, s := range synced {
			syncedMap[s.Model] = &pricing.ModelPricing{
				InputCostPerToken:                   s.InputCostPerToken,
				OutputCostPerToken:                  s.OutputCostPerToken,
				CacheReadInputTokenCost:             s.CacheReadCostPerToken,
				CacheCreationInputTokenCost:         s.CacheCreationCostPerToken,
				CacheCreationInputTokenCostAbove1hr: s.CacheCreationInputTokenCostAbove1hr,
				SupportsPromptCaching:               s.SupportsPromptCaching,
				Source:                              s.Source,
				UpdatedAt:                           s.UpdatedAt,
			}
		}
		p.pricing.LoadSyncedPrices(syncedMap)
	}

	if customs, err := store.LoadCustomPrices(); err == nil && len(customs) > 0 {
		customMap := make(map[string]*pricing.ModelPricing, len(customs))
		for _, c := range customs {
			customMap[c.Model] = &pricing.ModelPricing{
				InputCostPerToken:                   c.InputCostPerToken,
				OutputCostPerToken:                  c.OutputCostPerToken,
				CacheReadInputTokenCost:             c.CacheReadCostPerToken,
				CacheCreationInputTokenCost:         c.CacheCreationCostPerToken,
				CacheCreationInputTokenCostAbove1hr: c.CacheCreationInputTokenCostAbove1hr,
				SupportsPromptCaching:               c.SupportsPromptCaching,
				Source:                              c.Source,
				UpdatedAt:                           c.UpdatedAt,
				IsCustom:                            true,
			}
		}
		p.pricing.LoadCustomOverrides(customMap)
	}

	// Trigger periodic retention cleanup if configured
	if p.config.RetentionDays > 0 {
		go func() {
			cutoff := time.Now().AddDate(0, 0, -p.config.RetentionDays)
			_, _ = store.Cleanup(cutoff)
		}()
	}

	return nil
}

// HandleUsage processes usage.handle events delivered by CPA host.
func (p *Plugin) HandleUsage(payload []byte) ([]byte, error) {
	p.mu.RLock()
	store := p.store
	pricingEngine := p.pricing
	p.mu.RUnlock()

	if store == nil {
		return []byte("{}"), nil
	}

	var rec pluginapi.UsageRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal usage record: %w", err)
	}

	// Calculate cost. TotalTokens is passed along because the host forwards each
	// upstream's native token convention rather than a normalized one, and the
	// reported total is what lets those conventions be told apart.
	cost := pricingEngine.CalculateUsageCost(rec.Model, rec.RequestedAt, pricing.Usage{
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		TotalTokens:         rec.Detail.TotalTokens,
	})

	// Sanitize failure diagnostics
	var sanitizedBody string
	var failSummary string
	if rec.Failure.Body != "" {
		sanitizedBody = sanitize.SanitizeDiagnosticBody(rec.Failure.Body)
		failSummary = sanitize.FailSummaryFromBody(sanitizedBody)
	}

	// Extract metrics and recovery from upstream headers
	headerDerived := ParseResponseHeaders(rec.ResponseHeaders, rec.Failure.StatusCode, rec.RequestedAt)

	// Extract response model from payload or upstream response headers
	var rawFields struct {
		ResponseModel string `json:"response_model"`
		AltRespModel  string `json:"responseModel"`
	}
	_ = json.Unmarshal(payload, &rawFields)
	respModel := strings.TrimSpace(rawFields.ResponseModel)
	if respModel == "" {
		respModel = strings.TrimSpace(rawFields.AltRespModel)
	}
	if respModel == "" {
		respModel = strings.TrimSpace(headerDerived.ResponseModel)
	}

	modelMismatch := storage.DetectModelMismatch(rec.Alias, rec.Model, respModel)

	storageRec := &storage.Record{
		Provider:                          rec.Provider,
		BaseURL:                           rec.BaseURL,
		ExecutorType:                      rec.ExecutorType,
		Model:                             rec.Model,
		Alias:                             rec.Alias,
		ResponseModel:                     respModel,
		ModelMismatch:                     modelMismatch,
		APIKey:                            rec.APIKey,
		SessionID:                         rec.SessionID,
		ParentSessionID:                   rec.ParentSessionID,
		AuthID:                            rec.AuthID,
		AuthIndex:                         rec.AuthIndex,
		AuthType:                          rec.AuthType,
		Source:                            rec.Source,
		ReasoningEffort:                   rec.ReasoningEffort,
		ServiceTier:                       rec.ServiceTier,
		Generate:                          rec.Generate,
		RequestedAt:                       rec.RequestedAt,
		LatencyMs:                         rec.Latency.Milliseconds(),
		TTFTMs:                            rec.TTFT.Milliseconds(),
		Failed:                            rec.Failed,
		FailureStatusCode:                 rec.Failure.StatusCode,
		FailureBody:                       sanitizedBody,
		FailSummary:                       failSummary,
		HeaderErrorKind:                   headerDerived.ErrorKind,
		HeaderErrorCode:                   headerDerived.ErrorCode,
		HeaderTraceID:                     headerDerived.TraceID,
		HeaderQuotaRecoverAtMS:            headerDerived.QuotaRecoverAtMS,
		HeaderQuotaUsedPercent:            headerDerived.QuotaUsedPercent,
		HeaderQuotaWindowMinutes:          headerDerived.QuotaWindowMinutes,
		HeaderSecondaryQuotaRecoverAtMS:   headerDerived.SecondaryQuotaRecoverAtMS,
		HeaderSecondaryQuotaUsedPercent:   headerDerived.SecondaryQuotaUsedPercent,
		HeaderSecondaryQuotaWindowMinutes: headerDerived.SecondaryQuotaWindowMinutes,
		RateLimitReachedType:              headerDerived.RateLimitReachedType,
		PlanType:                          headerDerived.PlanType,
		InputTokens:                       rec.Detail.InputTokens,
		OutputTokens:                      rec.Detail.OutputTokens,
		ReasoningTokens:                   rec.Detail.ReasoningTokens,
		CachedTokens:                      rec.Detail.CachedTokens,
		CacheReadTokens:                   rec.Detail.CacheReadTokens,
		CacheCreationTokens:               rec.Detail.CacheCreationTokens,
		TotalTokens:                       rec.Detail.TotalTokens,
		InputCost:                         cost.InputCost,
		OutputCost:                        cost.OutputCost,
		CacheReadCost:                     cost.CacheReadCost,
		CacheCreationCost:                 cost.CacheCreationCost,
		TotalCost:                         cost.TotalCost,
		MatchedModel:                      cost.MatchedModel,
	}

	// Asynchronously enqueue for batch writing. The API read barrier flushes
	// accepted records before rendering the dashboard, while the host request
	// path remains non-blocking.
	store.Ingest(storageRec)

	return []byte("{}"), nil
}

type managementRegistrationResp struct {
	Routes    []pluginapi.ManagementRoute `json:"routes"`
	Resources []pluginapi.ResourceRoute   `json:"resources"`
}

// resourcePathPrefix is the host-owned prefix for browser-navigable plugin
// resources. CPA serves it without management authentication.
const resourcePathPrefix = "/v0/resource/plugins/"

// apiEndpoints lists the JSON endpoints, relative to whichever base they are
// mounted under.
var apiEndpoints = []string{
	"summary", "timeseries", "models", "keys",
	"auths", "accounts/health", "accounts/quota", "records", "filter-options",
	"diagnostics", "export", "import", "prices", "prices/sync", "prices/override",
	"cleanup", "ping",
}

// RegisterManagement exposes API routes and Web Dashboard UI resources.
//
// Management routes sit behind CPA's management authentication. Resource routes
// do not: the host serves /v0/resource/plugins/<id>/ without running the
// management middleware. Only the dashboard document is registered there by
// default, so the JSON endpoints stay behind authentication. Setting
// unauthenticated_api restores the endpoints on the resource path as well.
func (p *Plugin) RegisterManagement(payload []byte) ([]byte, error) {
	p.mu.RLock()
	unauthenticated := p.config.UnauthenticatedAPI
	p.mu.RUnlock()

	resp := managementRegistrationResp{
		Routes: []pluginapi.ManagementRoute{
			{Method: "GET", Path: "/usage/summary"},
			{Method: "GET", Path: "/usage/timeseries"},
			{Method: "GET", Path: "/usage/models"},
			{Method: "GET", Path: "/usage/keys"},
			{Method: "GET", Path: "/usage/auths"},
			{Method: "GET", Path: "/usage/accounts/health"},
			{Method: "GET", Path: "/usage/accounts/quota"},
			{Method: "GET", Path: "/usage/records"},
			{Method: "GET", Path: "/usage/filter-options"},
			{Method: "GET", Path: "/usage/diagnostics"},
			{Method: "GET", Path: "/usage/export"},
			{Method: "POST", Path: "/usage/import"},
			{Method: "GET", Path: "/usage/prices"},
			{Method: "GET", Path: "/usage/prices/sync"},
			{Method: "POST", Path: "/usage/prices/sync"},
			{Method: "GET", Path: "/usage/prices/override"},
			{Method: "POST", Path: "/usage/prices/override"},
			{Method: "DELETE", Path: "/usage/prices/override"},
			{Method: "POST", Path: "/usage/cleanup"},
			{Method: "GET", Path: "/usage/ping"},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/dashboard",
				Menu:        "使用量看板 (Usage)",
				Description: "模型调用、Token 消耗、费用估算与请求日志",
			},
		},
	}

	if unauthenticated {
		for _, name := range apiEndpoints {
			resp.Resources = append(resp.Resources, pluginapi.ResourceRoute{Path: "/api/" + name})
		}
	}

	return json.Marshal(resp)
}

// HandleManagement processes incoming Management API & Resource requests.
func (p *Plugin) HandleManagement(payload []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal management request: %w", err)
	}

	p.mu.RLock()
	handler := p.apiHandler
	unauthenticated := p.config.UnauthenticatedAPI
	p.mu.RUnlock()

	path := strings.TrimSpace(req.Path)
	cleanPath := strings.TrimRight(path, "/")

	// 1. Check if serving the Web UI HTML
	if strings.HasSuffix(cleanPath, "/dashboard") ||
		strings.HasSuffix(cleanPath, "/dashboard/index.html") ||
		strings.HasSuffix(cleanPath, "/cpa-usage") ||
		cleanPath == "" ||
		strings.HasSuffix(cleanPath, "/index.html") {

		bootCfg := web.BootConfig{UnauthenticatedAPI: unauthenticated}
		etag := web.DashboardETag(bootCfg)

		// The document is ~250KB and identical until the plugin or its boot
		// config changes. Answer conditional requests with 304 so refreshes
		// skip the transfer and the browser's HTML/JS re-parse entirely.
		if match := strings.TrimSpace(req.Headers.Get("If-None-Match")); match == etag {
			resp := pluginapi.ManagementResponse{
				StatusCode: http.StatusNotModified,
				Headers: http.Header{
					"Etag":          []string{etag},
					"Cache-Control": []string{"no-cache, must-revalidate"},
				},
			}
			return json.Marshal(resp)
		}

		resp := pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type": []string{"text/html; charset=utf-8"},
				// no-cache (not no-store) lets the browser keep the copy so a
				// conditional request can be made cheap; revalidation is a 304.
				"Cache-Control": []string{"no-cache, must-revalidate"},
				"Etag":          []string{etag},
			},
			Body: web.Dashboard(bootCfg),
		}
		return json.Marshal(resp)
	}

	// 2. Refuse JSON requests that arrived on the unauthenticated resource path
	// unless it was explicitly opened up. The routes are not registered there in
	// that case, so this only guards against a stale route table after a reload.
	if !unauthenticated && strings.HasPrefix(cleanPath, resourcePathPrefix) {
		resp := pluginapi.ManagementResponse{
			StatusCode: http.StatusNotFound,
			Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"not_found","message":"the JSON API is served under /v0/management/usage/ and requires a management key"}`),
		}
		return json.Marshal(resp)
	}

	// 3. Dispatch to JSON API Handler
	if handler == nil {
		resp := pluginapi.ManagementResponse{
			StatusCode: http.StatusServiceUnavailable,
			Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"service_unavailable","message":"storage is not initialized"}`),
		}
		return json.Marshal(resp)
	}

	apiResp := handler.Handle(req.Method, req.Path, req.Query, req.Body)
	resp := pluginapi.ManagementResponse{
		StatusCode: apiResp.StatusCode,
		Headers:    apiResp.Headers,
		Body:       apiResp.Body,
	}
	return json.Marshal(resp)
}

// Shutdown gracefully flushes and closes database storage.
func (p *Plugin) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store != nil {
		_ = p.store.Close()
		p.store = nil
	}
}
