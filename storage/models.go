package storage

import "time"

// Record represents an ingested usage log with billing and token metrics.
type Record struct {
	ID                                int64     `json:"id"`
	Provider                          string    `json:"provider"`
	BaseURL                           string    `json:"base_url,omitempty"`
	ExecutorType                      string    `json:"executor_type,omitempty"`
	Model                             string    `json:"model"`
	Alias                             string    `json:"alias,omitempty"`
	APIKey                            string    `json:"api_key,omitempty"`
	SessionID                         string    `json:"session_id,omitempty"`
	ParentSessionID                   string    `json:"parent_session_id,omitempty"`
	AuthID                            string    `json:"auth_id,omitempty"`
	AuthIndex                         string    `json:"auth_index,omitempty"`
	AuthType                          string    `json:"auth_type,omitempty"`
	Source                            string    `json:"source,omitempty"`
	ReasoningEffort                   string    `json:"reasoning_effort,omitempty"`
	ServiceTier                       string    `json:"service_tier,omitempty"`
	Generate                          bool      `json:"generate"`
	RequestedAt                       time.Time `json:"requested_at"`
	LatencyMs                         int64     `json:"latency_ms"`
	TTFTMs                            int64     `json:"ttft_ms"`
	Failed                            bool      `json:"failed"`
	FailureStatusCode                 int       `json:"failure_status_code,omitempty"`
	FailureBody                       string    `json:"failure_body,omitempty"`
	FailSummary                       string    `json:"fail_summary,omitempty"`
	HeaderErrorKind                   string    `json:"header_error_kind,omitempty"`
	HeaderErrorCode                   string    `json:"header_error_code,omitempty"`
	HeaderTraceID                     string    `json:"header_trace_id,omitempty"`
	HeaderQuotaRecoverAtMS            int64     `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent            *float64  `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaWindowMinutes          *float64  `json:"header_quota_window_minutes,omitempty"`
	HeaderSecondaryQuotaRecoverAtMS   int64     `json:"header_secondary_quota_recover_at_ms,omitempty"`
	HeaderSecondaryQuotaUsedPercent   *float64  `json:"header_secondary_quota_used_percent,omitempty"`
	HeaderSecondaryQuotaWindowMinutes *float64  `json:"header_secondary_quota_window_minutes,omitempty"`
	RateLimitReachedType              string    `json:"rate_limit_reached_type,omitempty"`
	PlanType                          string    `json:"plan_type,omitempty"`
	ResponseModel                     string    `json:"response_model,omitempty"`
	ModelMismatch                     bool      `json:"model_mismatch,omitempty"`
	InputTokens                       int64     `json:"input_tokens"`
	OutputTokens                      int64     `json:"output_tokens"`
	ReasoningTokens                   int64     `json:"reasoning_tokens"`
	CachedTokens                      int64     `json:"cached_tokens"`
	CacheReadTokens                   int64     `json:"cache_read_tokens"`
	CacheCreationTokens               int64     `json:"cache_creation_tokens"`
	TotalTokens                       int64     `json:"total_tokens"`
	InputCost                         float64   `json:"input_cost"`
	OutputCost                        float64   `json:"output_cost"`
	CacheReadCost                     float64   `json:"cache_read_cost"`
	CacheCreationCost                 float64   `json:"cache_creation_cost"`
	TotalCost                         float64   `json:"total_cost"`
	MatchedModel                      string    `json:"matched_model,omitempty"`
}

// QueryFilter defines filtering criteria for aggregation queries.
type QueryFilter struct {
	StartTime     *time.Time `json:"start_time,omitempty"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	APIKey        string     `json:"api_key,omitempty"`
	Model         string     `json:"model,omitempty"`
	Provider      string     `json:"provider,omitempty"`
	AuthID        string     `json:"auth_id,omitempty"`
	Failed        *bool      `json:"failed,omitempty"`
	ModelMismatch *bool      `json:"model_mismatch,omitempty"`
	Search        string     `json:"search,omitempty"`
}

// RecordQueryFilter defines criteria for paginated raw record queries.
type RecordQueryFilter struct {
	QueryFilter
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// SummaryStats contains aggregated metrics for an overview display.
type SummaryStats struct {
	TotalRequests       int64   `json:"total_requests"`
	SuccessRequests     int64   `json:"success_requests"`
	FailedRequests      int64   `json:"failed_requests"`
	TotalTokens         int64   `json:"total_tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalCost           float64 `json:"total_cost"`
	AvgLatencyMs        float64 `json:"avg_latency_ms"`
}

// TimeSeriesPoint represents metrics bucketed into a specific time window.
type TimeSeriesPoint struct {
	Timestamp    string  `json:"timestamp"`
	Requests     int64   `json:"requests"`
	SuccessCount int64   `json:"success_count"`
	FailedCount  int64   `json:"failed_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// ModelStat represents metrics grouped by model.
type ModelStat struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	TotalTokens  int64   `json:"total_tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// APIKeyStat represents metrics grouped by client API key.
type APIKeyStat struct {
	APIKey      string    `json:"api_key"`
	Requests    int64     `json:"requests"`
	TotalTokens int64     `json:"total_tokens"`
	TotalCost   float64   `json:"total_cost"`
	LastUsedAt  time.Time `json:"last_used_at"`
}

// AuthStat represents metrics grouped by credential / account.
type AuthStat struct {
	AuthID       string  `json:"auth_id"`
	AuthType     string  `json:"auth_type"`
	Provider     string  `json:"provider"`
	Requests     int64   `json:"requests"`
	FailedCount  int64   `json:"failed_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// FilterOptions provides list of distinct values for UI filter dropdowns.
type FilterOptions struct {
	Models    []string `json:"models"`
	Providers []string `json:"providers"`
	APIKeys   []string `json:"api_keys"`
	AuthIDs   []string `json:"auth_ids"`
}

// DiagnosticErrorGroup represents count of errors grouped by an attribute.
type DiagnosticErrorGroup struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// DiagnosticStats provides error distributions and recent error telemetry.
type DiagnosticStats struct {
	TotalRequests   int64                  `json:"total_requests"`
	FailedRequests  int64                  `json:"failed_requests"`
	FailureRate     float64                `json:"failure_rate"`
	ModelMismatches int64                  `json:"model_mismatches"`
	ByStatusCode    []DiagnosticErrorGroup `json:"by_status_code"`
	ByErrorKind     []DiagnosticErrorGroup `json:"by_error_kind"`
	ByErrorCode     []DiagnosticErrorGroup `json:"by_error_code"`
	ByProvider      []DiagnosticErrorGroup `json:"by_provider"`
	ByModel         []DiagnosticErrorGroup `json:"by_model"`
	ByMismatchPair  []DiagnosticErrorGroup `json:"by_mismatch_pair,omitempty"`
	RecentErrors    []*Record              `json:"recent_errors"`
}

// CustomPriceRecord stores a persistent custom price override.
type CustomPriceRecord struct {
	Model                               string  `json:"model"`
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	CacheReadCostPerToken               float64 `json:"cache_read_cost_per_token"`
	CacheCreationCostPerToken           float64 `json:"cache_creation_cost_per_token"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	Source                              string  `json:"source,omitempty"`
	UpdatedAt                           int64   `json:"updated_at"`
}

// SyncedPriceRecord stores a persistent synced price from remote sources.
type SyncedPriceRecord struct {
	Model                               string  `json:"model"`
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	CacheReadCostPerToken               float64 `json:"cache_read_cost_per_token"`
	CacheCreationCostPerToken           float64 `json:"cache_creation_cost_per_token"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	Source                              string  `json:"source,omitempty"`
	UpdatedAt                           int64   `json:"updated_at"`
}

// Account health status constants.
const (
	HealthStatusAvailable    = "available"
	HealthStatusLowQuota     = "low_quota"
	HealthStatusExhausted    = "exhausted"
	HealthStatusCooldown     = "cooldown"
	HealthStatusReauthNeeded = "reauth_needed"
	HealthStatusError        = "error"
	HealthStatusUnknown      = "unknown"
)

// AccountHealth represents evaluated operational status and telemetry for an account/credential.
type AccountHealth struct {
	AuthID                   string    `json:"auth_id"`
	AuthType                 string    `json:"auth_type,omitempty"`
	Provider                 string    `json:"provider"`
	PlanType                 string    `json:"plan_type,omitempty"`
	Status                   string    `json:"status"` // available, low_quota, exhausted, cooldown, reauth_needed, error, unknown
	StatusReason             string    `json:"status_reason"`
	HealthScore              float64   `json:"health_score"` // 0 - 100
	TotalRequests            int64     `json:"total_requests"`
	SuccessRequests          int64     `json:"success_requests"`
	FailedRequests           int64     `json:"failed_requests"`
	SuccessRate              float64   `json:"success_rate"`
	AvgLatencyMs             float64   `json:"avg_latency_ms"`
	LastUsedAt               time.Time `json:"last_used_at"`
	LastStatusCode           int       `json:"last_status_code,omitempty"`
	LastErrorSummary         string    `json:"last_error_summary,omitempty"`
	LastErrorKind            string    `json:"last_error_kind,omitempty"`
	QuotaUsedPercent         *float64  `json:"quota_used_percent,omitempty"`
	QuotaRecoverAtMS         int64     `json:"quota_recover_at_ms,omitempty"`
	CooldownRemainingSeconds int64     `json:"cooldown_remaining_seconds"`
	InCooldown               bool      `json:"in_cooldown"`
	// RateLimited is true when the latest record carries real rate-limit
	// evidence (429 / rate_limit headers / reached-window marker).
	RateLimited bool `json:"rate_limited"`
	// ResetRemainingSeconds is the informational countdown until the current
	// quota window resets. A healthy, actively used account always has one; it
	// is NOT a cooldown and must never be rendered as one.
	ResetRemainingSeconds int64 `json:"reset_remaining_seconds"`
}

// AccountHealthSummary aggregates health counts across all accounts.
type AccountHealthSummary struct {
	TotalAccounts  int `json:"total_accounts"`
	HealthyCount   int `json:"healthy_count"`
	LowQuotaCount  int `json:"low_quota_count"`
	CooldownCount  int `json:"cooldown_count"`
	ExhaustedCount int `json:"exhausted_count"`
	ReauthCount    int `json:"reauth_count"`
	ErrorCount     int `json:"error_count"`
	UnknownCount   int `json:"unknown_count"`
}

// AccountHealthResponse is the response payload for /api/accounts/health.
type AccountHealthResponse struct {
	Summary       AccountHealthSummary `json:"summary"`
	Accounts      []AccountHealth      `json:"accounts"`
	TotalAccounts int                  `json:"total_accounts"`
}

// AccountQuotaWindowDetail holds timing and usage metrics for a specific quota window.
// AccountQuotaForecast is a conservative estimate derived from recent quota
// percentage observations for the same account and window. A forecast is
// explicitly unavailable when the observations are too sparse, stale, or span
// an ambiguous reset boundary.
type AccountQuotaForecast struct {
	Status                      string  `json:"status"` // available, after_reset, exhausted, insufficient_data
	EstimatedExhaustionAtMS     int64   `json:"estimated_exhaustion_at_ms,omitempty"`
	EstimatedExhaustionAfterSec int64   `json:"estimated_exhaustion_after_seconds,omitempty"`
	BurnRatePercentPerHour      float64 `json:"burn_rate_percent_per_hour,omitempty"`
	SampleCount                 int     `json:"sample_count"`
}

// AccountQuotaWindowDetail holds timing and usage metrics for a specific quota window.
type AccountQuotaWindowDetail struct {
	WindowKind       string                `json:"window_kind"` // five_hour, weekly, monthly, unknown
	DurationSeconds  int64                 `json:"duration_seconds"`
	UsedPercent      *float64              `json:"used_percent,omitempty"`
	RemainingPercent *float64              `json:"remaining_percent,omitempty"`
	ResetAtMS        int64                 `json:"reset_at_ms,omitempty"`
	ResetAfterSec    int64                 `json:"reset_after_seconds,omitempty"`
	IsExhausted      bool                  `json:"is_exhausted"`
	Forecast         *AccountQuotaForecast `json:"forecast,omitempty"`
	// Observed consumption inside this window. Upstream only reports a usage
	// percentage, so the absolute allowance below is back-calculated from what
	// this window actually recorded. Always an estimate, never an accounting
	// figure: it only counts requests that passed through this proxy.
	ConsumedTokens             int64  `json:"consumed_tokens,omitempty"`
	ConsumedRequests           int64  `json:"consumed_requests,omitempty"`
	EstimatedTotalTokens       *int64 `json:"estimated_total_tokens,omitempty"`
	EstimatedRemainingTokens   *int64 `json:"estimated_remaining_tokens,omitempty"`
	EstimatedRemainingRequests *int64 `json:"estimated_remaining_requests,omitempty"`
}

// AccountQuotaDetail represents quota snapshots and rate limit windows for an account.
type AccountQuotaDetail struct {
	AuthID                   string                    `json:"auth_id"`
	AuthType                 string                    `json:"auth_type,omitempty"`
	Provider                 string                    `json:"provider"`
	PlanType                 string                    `json:"plan_type,omitempty"`
	PrimaryWindow            *AccountQuotaWindowDetail `json:"primary_window,omitempty"`
	SecondaryWindow          *AccountQuotaWindowDetail `json:"secondary_window,omitempty"`
	SummaryUsedPercent       *float64                  `json:"summary_used_percent,omitempty"`
	SummaryRecoverAtMS       int64                     `json:"summary_recover_at_ms,omitempty"`
	ReachedWindowKind        string                    `json:"reached_window_kind,omitempty"`
	ReachedWindowSource      string                    `json:"reached_window_source,omitempty"`
	CooldownRemainingSeconds int64                     `json:"cooldown_remaining_seconds"`
	InCooldown               bool                      `json:"in_cooldown"`
	RateLimited              bool                      `json:"rate_limited"`
	ResetRemainingSeconds    int64                     `json:"reset_remaining_seconds"`
	TotalTokensRecent        int64                     `json:"total_tokens_recent"`
	TotalCostRecent          float64                   `json:"total_cost_recent"`
	LastObservedAt           time.Time                 `json:"last_observed_at"`
}

// AccountQuotaResponse is the response payload for /api/accounts/quota.
type AccountQuotaResponse struct {
	Quotas        []AccountQuotaDetail `json:"quotas"`
	TotalAccounts int                  `json:"total_accounts"`
}
