package storage

import "time"

// Record represents an ingested usage log with billing and token metrics.
type Record struct {
	ID                  int64     `json:"id"`
	Provider            string    `json:"provider"`
	BaseURL             string    `json:"base_url,omitempty"`
	ExecutorType        string    `json:"executor_type,omitempty"`
	Model               string    `json:"model"`
	Alias               string    `json:"alias,omitempty"`
	APIKey              string    `json:"api_key,omitempty"`
	SessionID           string    `json:"session_id,omitempty"`
	ParentSessionID     string    `json:"parent_session_id,omitempty"`
	AuthID              string    `json:"auth_id,omitempty"`
	AuthIndex           string    `json:"auth_index,omitempty"`
	AuthType            string    `json:"auth_type,omitempty"`
	Source              string    `json:"source,omitempty"`
	ReasoningEffort     string    `json:"reasoning_effort,omitempty"`
	ServiceTier         string    `json:"service_tier,omitempty"`
	Generate            bool      `json:"generate"`
	RequestedAt         time.Time `json:"requested_at"`
	LatencyMs           int64     `json:"latency_ms"`
	TTFTMs              int64     `json:"ttft_ms"`
	Failed              bool      `json:"failed"`
	FailureStatusCode   int       `json:"failure_status_code,omitempty"`
	FailureBody         string    `json:"failure_body,omitempty"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	ReasoningTokens     int64     `json:"reasoning_tokens"`
	CachedTokens        int64     `json:"cached_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	TotalTokens         int64     `json:"total_tokens"`
	InputCost           float64   `json:"input_cost"`
	OutputCost          float64   `json:"output_cost"`
	CacheReadCost       float64   `json:"cache_read_cost"`
	CacheCreationCost   float64   `json:"cache_creation_cost"`
	TotalCost           float64   `json:"total_cost"`
	MatchedModel        string    `json:"matched_model,omitempty"`
}

// QueryFilter defines filtering criteria for aggregation queries.
type QueryFilter struct {
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	APIKey    string     `json:"api_key,omitempty"`
	Model     string     `json:"model,omitempty"`
	Provider  string     `json:"provider,omitempty"`
	AuthID    string     `json:"auth_id,omitempty"`
	Failed    *bool      `json:"failed,omitempty"`
	Search    string     `json:"search,omitempty"`
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
