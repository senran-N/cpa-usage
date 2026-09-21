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

	// Calculate cost
	cost := pricingEngine.CalculateCost(
		rec.Model,
		rec.RequestedAt,
		rec.Detail.InputTokens,
		rec.Detail.OutputTokens,
		rec.Detail.ReasoningTokens,
		rec.Detail.CacheReadTokens,
		rec.Detail.CacheCreationTokens,
	)

	storageRec := &storage.Record{
		Provider:            rec.Provider,
		BaseURL:             rec.BaseURL,
		ExecutorType:        rec.ExecutorType,
		Model:               rec.Model,
		Alias:               rec.Alias,
		APIKey:              rec.APIKey,
		SessionID:           rec.SessionID,
		ParentSessionID:     rec.ParentSessionID,
		AuthID:              rec.AuthID,
		AuthIndex:           rec.AuthIndex,
		AuthType:            rec.AuthType,
		Source:              rec.Source,
		ReasoningEffort:     rec.ReasoningEffort,
		ServiceTier:         rec.ServiceTier,
		Generate:            rec.Generate,
		RequestedAt:         rec.RequestedAt,
		LatencyMs:           rec.Latency.Milliseconds(),
		TTFTMs:              rec.TTFT.Milliseconds(),
		Failed:              rec.Failed,
		FailureStatusCode:   rec.Failure.StatusCode,
		FailureBody:         rec.Failure.Body,
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		CachedTokens:        rec.Detail.CachedTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		TotalTokens:         rec.Detail.TotalTokens,
		InputCost:           cost.InputCost,
		OutputCost:          cost.OutputCost,
		CacheReadCost:       cost.CacheReadCost,
		CacheCreationCost:   cost.CacheCreationCost,
		TotalCost:           cost.TotalCost,
		MatchedModel:        cost.MatchedModel,
	}

	// Asynchronously enqueue for batch writing
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
	"auths", "records", "filter-options", "cleanup", "ping",
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
			{Method: "GET", Path: "/usage/records"},
			{Method: "GET", Path: "/usage/filter-options"},
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

		resp := pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
			},
			Body: web.Dashboard(web.BootConfig{UnauthenticatedAPI: unauthenticated}),
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
