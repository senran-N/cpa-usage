package storage

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GetAccountsHealth evaluates health, operational status, error telemetry, and quota cooldowns for accounts.
func (s *Storage) GetAccountsHealth(filter QueryFilter, now time.Time) (*AccountHealthResponse, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	authStats, err := s.GetAuthStats(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth stats: %w", err)
	}

	latestMap, err := s.fetchLatestRecordsForAccounts(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest records: %w", err)
	}

	resp := &AccountHealthResponse{
		Accounts:      make([]AccountHealth, 0, len(authStats)),
		TotalAccounts: len(authStats),
	}

	for _, stat := range authStats {
		latestRec := latestMap[stat.AuthID]
		h := EvaluateAccountHealth(stat, latestRec, now)
		resp.Accounts = append(resp.Accounts, h)

		switch h.Status {
		case HealthStatusAvailable:
			resp.Summary.HealthyCount++
		case HealthStatusLowQuota:
			resp.Summary.LowQuotaCount++
		case HealthStatusCooldown:
			resp.Summary.CooldownCount++
		case HealthStatusExhausted:
			resp.Summary.ExhaustedCount++
		case HealthStatusReauthNeeded:
			resp.Summary.ReauthCount++
		case HealthStatusError:
			resp.Summary.ErrorCount++
		default:
			resp.Summary.UnknownCount++
		}
	}
	resp.Summary.TotalAccounts = len(resp.Accounts)

	// Sort accounts: attention-requiring accounts first, then descending by requests
	statusPriority := func(st string) int {
		switch st {
		case HealthStatusReauthNeeded:
			return 1
		case HealthStatusExhausted:
			return 2
		case HealthStatusCooldown:
			return 3
		case HealthStatusError:
			return 4
		case HealthStatusLowQuota:
			return 5
		case HealthStatusAvailable:
			return 6
		default:
			return 7
		}
	}

	sort.Slice(resp.Accounts, func(i, j int) bool {
		pi := statusPriority(resp.Accounts[i].Status)
		pj := statusPriority(resp.Accounts[j].Status)
		if pi != pj {
			return pi < pj
		}
		return resp.Accounts[i].TotalRequests > resp.Accounts[j].TotalRequests
	})

	return resp, nil
}

// GetAccountsQuota returns detailed quota windows and cooldown countdowns for accounts.
func (s *Storage) GetAccountsQuota(filter QueryFilter, now time.Time) (*AccountQuotaResponse, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	authStats, err := s.GetAuthStats(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth stats: %w", err)
	}

	latestMap, err := s.fetchLatestRecordsForAccounts(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest records: %w", err)
	}

	resp := &AccountQuotaResponse{
		Quotas:        make([]AccountQuotaDetail, 0, len(authStats)),
		TotalAccounts: len(authStats),
	}

	for _, stat := range authStats {
		latestRec := latestMap[stat.AuthID]
		q := EvaluateAccountQuota(stat, latestRec, now)
		resp.Quotas = append(resp.Quotas, q)
	}

	sort.Slice(resp.Quotas, func(i, j int) bool {
		// Put accounts in cooldown or exhausted first
		if resp.Quotas[i].InCooldown != resp.Quotas[j].InCooldown {
			return resp.Quotas[i].InCooldown
		}
		return resp.Quotas[i].TotalTokensRecent > resp.Quotas[j].TotalTokensRecent
	})

	return resp, nil
}

// EvaluateAccountHealth determines health score, failure classification, and cooldown status.
func EvaluateAccountHealth(stat *AuthStat, latestRec *Record, now time.Time) AccountHealth {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowMS := now.UnixMilli()

	h := AccountHealth{
		Status:       HealthStatusUnknown,
		StatusReason: "暂无请求记录",
		HealthScore:  100.0,
	}

	if stat != nil {
		h.AuthID = stat.AuthID
		h.AuthType = stat.AuthType
		h.Provider = stat.Provider
		h.TotalRequests = stat.Requests
		h.FailedRequests = stat.FailedCount
		h.SuccessRequests = stat.Requests - stat.FailedCount
		if h.SuccessRequests < 0 {
			h.SuccessRequests = 0
		}
		if stat.Requests > 0 {
			h.SuccessRate = float64(h.SuccessRequests) / float64(stat.Requests)
		}
		h.AvgLatencyMs = stat.AvgLatencyMs
	}

	if latestRec == nil {
		h.HealthScore = 0.0
		return h
	}

	if h.Provider == "" {
		h.Provider = latestRec.Provider
	}
	if h.AuthType == "" {
		h.AuthType = latestRec.AuthType
	}
	h.PlanType = latestRec.PlanType
	h.LastUsedAt = latestRec.RequestedAt
	h.LastStatusCode = latestRec.FailureStatusCode
	h.LastErrorSummary = latestRec.FailSummary
	h.LastErrorKind = latestRec.HeaderErrorKind

	// Quota telemetry
	h.QuotaUsedPercent = clampPercent(latestRec.HeaderQuotaUsedPercent)
	h.QuotaRecoverAtMS = latestRec.HeaderQuotaRecoverAtMS

	// Cooldown calculation with past-timestamp safety
	if latestRec.HeaderQuotaRecoverAtMS > nowMS {
		h.CooldownRemainingSeconds = (latestRec.HeaderQuotaRecoverAtMS - nowMS + 999) / 1000
		h.InCooldown = true
	} else {
		h.CooldownRemainingSeconds = 0
		h.InCooldown = false
	}

	// 1. Re-authentication / Credential failure
	if isAuthFailure(latestRec.FailureStatusCode, latestRec.HeaderErrorKind, latestRec.HeaderErrorCode, latestRec.FailSummary) {
		h.Status = HealthStatusReauthNeeded
		detail := latestRec.HeaderErrorCode
		if detail == "" {
			detail = latestRec.FailSummary
		}
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", latestRec.FailureStatusCode)
		}
		h.StatusReason = fmt.Sprintf("凭据失效需重登 (%s)", detail)
		h.HealthScore = 0.0
		return h
	}

	// 2. Cooldown active
	if h.InCooldown {
		h.Status = HealthStatusCooldown
		if h.CooldownRemainingSeconds > 14400 && h.CooldownRemainingSeconds <= 18300 {
			h.StatusReason = fmt.Sprintf("5H 窗口限流冷却中 (剩余 %dm)", h.CooldownRemainingSeconds/60)
		} else if h.CooldownRemainingSeconds > 18300 {
			h.StatusReason = fmt.Sprintf("周/月配额限流冷却中 (剩余 %dh)", h.CooldownRemainingSeconds/3600)
		} else {
			h.StatusReason = fmt.Sprintf("限流冷却中 (剩余 %ds)", h.CooldownRemainingSeconds)
		}
	} else if h.QuotaUsedPercent != nil && *h.QuotaUsedPercent >= 100.0 {
		// 3. Quota exhausted
		h.Status = HealthStatusExhausted
		h.StatusReason = "配额已耗尽 (100%)"
	} else if latestRec.Failed && latestRec.FailureStatusCode == 429 {
		h.Status = HealthStatusExhausted
		h.StatusReason = "触发限流 (429 Too Many Requests)"
	} else if h.QuotaUsedPercent != nil && *h.QuotaUsedPercent >= 85.0 {
		// 4. Low quota warning
		h.Status = HealthStatusLowQuota
		h.StatusReason = fmt.Sprintf("额度不足：已使用 %.1f%%", *h.QuotaUsedPercent)
	} else if latestRec.Failed && (latestRec.FailureStatusCode >= 500 || latestRec.HeaderErrorKind == "server_error") {
		// 5. Server or infrastructure error
		h.Status = HealthStatusError
		h.StatusReason = fmt.Sprintf("上游服务异常 (HTTP %d)", latestRec.FailureStatusCode)
	} else if stat != nil && stat.Requests >= 3 && float64(stat.FailedCount)/float64(stat.Requests) > 0.5 {
		h.Status = HealthStatusError
		h.StatusReason = fmt.Sprintf("近期错误率过高 (%.1f%%)", float64(stat.FailedCount)/float64(stat.Requests)*100)
	} else {
		// 6. Healthy and available
		h.Status = HealthStatusAvailable
		h.StatusReason = "正常可用"
	}

	// Calculate robust health score (0 - 100)
	score := h.SuccessRate * 100.0

	// Latency penalty
	if h.AvgLatencyMs > 2000 {
		penalty := (h.AvgLatencyMs - 2000) / 400.0
		if penalty > 25.0 {
			penalty = 25.0
		}
		score -= penalty
	}

	// Quota used penalty
	if h.QuotaUsedPercent != nil && *h.QuotaUsedPercent > 75.0 {
		penalty := (*h.QuotaUsedPercent - 75.0) * 1.5
		score -= penalty
	}

	// Cap based on status
	switch h.Status {
	case HealthStatusExhausted:
		if score > 15.0 {
			score = 15.0
		}
	case HealthStatusCooldown:
		if score > 35.0 {
			score = 35.0
		}
	case HealthStatusError:
		if score > 40.0 {
			score = 40.0
		}
	case HealthStatusLowQuota:
		if score > 70.0 {
			score = 70.0
		}
	}

	if score < 0.0 {
		score = 0.0
	} else if score > 100.0 {
		score = 100.0
	}
	h.HealthScore = score

	return h
}

// EvaluateAccountQuota constructs primary and secondary window snapshots for an account.
func EvaluateAccountQuota(stat *AuthStat, latestRec *Record, now time.Time) AccountQuotaDetail {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowMS := now.UnixMilli()

	q := AccountQuotaDetail{}
	if stat != nil {
		q.AuthID = stat.AuthID
		q.AuthType = stat.AuthType
		q.Provider = stat.Provider
		q.TotalTokensRecent = stat.TotalTokens
		q.TotalCostRecent = stat.TotalCost
	}

	if latestRec == nil {
		return q
	}

	if q.Provider == "" {
		q.Provider = latestRec.Provider
	}
	if q.AuthType == "" {
		q.AuthType = latestRec.AuthType
	}
	q.PlanType = latestRec.PlanType
	q.LastObservedAt = latestRec.RequestedAt

	// Primary Window (default 5-hour)
	var primWindow *AccountQuotaWindowDetail
	if latestRec.HeaderQuotaUsedPercent != nil || latestRec.HeaderQuotaRecoverAtMS > 0 {
		primUsed := clampPercent(latestRec.HeaderQuotaUsedPercent)
		var primRem *float64
		isExhausted := false
		if primUsed != nil {
			rem := 100.0 - *primUsed
			if rem < 0 {
				rem = 0
			}
			primRem = &rem
			if *primUsed >= 100.0 {
				isExhausted = true
			}
		}

		var primResetSec int64
		if latestRec.HeaderQuotaRecoverAtMS > nowMS {
			primResetSec = (latestRec.HeaderQuotaRecoverAtMS - nowMS + 999) / 1000
		}

		primWindow = &AccountQuotaWindowDetail{
			WindowKind:       "five_hour",
			DurationSeconds:  18000,
			UsedPercent:      primUsed,
			RemainingPercent: primRem,
			ResetAtMS:        latestRec.HeaderQuotaRecoverAtMS,
			ResetAfterSec:    primResetSec,
			IsExhausted:      isExhausted,
		}
	}
	q.PrimaryWindow = primWindow

	// Secondary Window (default weekly)
	var secWindow *AccountQuotaWindowDetail
	if latestRec.HeaderSecondaryQuotaUsedPercent != nil || latestRec.HeaderSecondaryQuotaRecoverAtMS > 0 {
		secUsed := clampPercent(latestRec.HeaderSecondaryQuotaUsedPercent)
		var secRem *float64
		isExhausted := false
		if secUsed != nil {
			rem := 100.0 - *secUsed
			if rem < 0 {
				rem = 0
			}
			secRem = &rem
			if *secUsed >= 100.0 {
				isExhausted = true
			}
		}

		var secResetSec int64
		if latestRec.HeaderSecondaryQuotaRecoverAtMS > nowMS {
			secResetSec = (latestRec.HeaderSecondaryQuotaRecoverAtMS - nowMS + 999) / 1000
		}

		secWindow = &AccountQuotaWindowDetail{
			WindowKind:       "weekly",
			DurationSeconds:  604800,
			UsedPercent:      secUsed,
			RemainingPercent: secRem,
			ResetAtMS:        latestRec.HeaderSecondaryQuotaRecoverAtMS,
			ResetAfterSec:    secResetSec,
			IsExhausted:      isExhausted,
		}
	}
	q.SecondaryWindow = secWindow

	// Summary values
	q.SummaryUsedPercent = q.PrimaryWindow.GetUsedPercent()
	if q.SecondaryWindow != nil && q.SecondaryWindow.UsedPercent != nil {
		if q.SummaryUsedPercent == nil || *q.SecondaryWindow.UsedPercent > *q.SummaryUsedPercent {
			q.SummaryUsedPercent = q.SecondaryWindow.UsedPercent
		}
	}

	q.SummaryRecoverAtMS = latestRec.HeaderQuotaRecoverAtMS
	if q.SummaryRecoverAtMS <= 0 && latestRec.HeaderSecondaryQuotaRecoverAtMS > 0 {
		q.SummaryRecoverAtMS = latestRec.HeaderSecondaryQuotaRecoverAtMS
	}

	if q.SummaryRecoverAtMS > nowMS {
		q.CooldownRemainingSeconds = (q.SummaryRecoverAtMS - nowMS + 999) / 1000
		q.InCooldown = true
	} else {
		q.CooldownRemainingSeconds = 0
		q.InCooldown = false
	}

	return q
}

// GetUsedPercent safely returns pointer to used percent or nil.
func (w *AccountQuotaWindowDetail) GetUsedPercent() *float64 {
	if w == nil {
		return nil
	}
	return w.UsedPercent
}

func (s *Storage) fetchLatestRecordsForAccounts(filter QueryFilter) (map[string]*Record, error) {
	where, args := buildWhereClause(filter)
	if where == "" {
		where = " WHERE auth_id != '' "
	} else {
		where += " AND auth_id != '' "
	}

	query := fmt.Sprintf(`
		SELECT
			r.id, r.provider, r.base_url, r.executor_type, r.model, r.alias,
			r.api_key, r.session_id, r.parent_session_id, r.auth_id, r.auth_index,
			r.auth_type, r.source, r.reasoning_effort, r.service_tier, r.generate,
			r.requested_at, r.latency_ms, r.ttft_ms, r.failed,
			r.failure_status_code, r.failure_body, r.fail_summary, r.header_error_kind, r.header_error_code,
			r.header_trace_id, r.header_quota_recover_at_ms, r.header_quota_used_percent,
			r.header_secondary_quota_recover_at_ms, r.header_secondary_quota_used_percent, r.plan_type,
			r.input_tokens, r.output_tokens, r.reasoning_tokens,
			r.cached_tokens, r.cache_read_tokens, r.cache_creation_tokens, r.total_tokens,
			r.input_cost, r.output_cost, r.cache_read_cost, r.cache_creation_cost, r.total_cost,
			r.matched_model
		FROM usage_records r
		INNER JOIN (
			SELECT auth_id, MAX(id) AS max_id
			FROM usage_records
			%s
			GROUP BY auth_id
		) latest ON r.id = latest.max_id
	`, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query latest records by auth_id: %w", err)
	}
	defer rows.Close()

	records := make(map[string]*Record)
	for rows.Next() {
		r := &Record{}
		var genInt, failedInt int
		var reqAtStr string
		var quotaUsedPct, secQuotaUsedPct sql.NullFloat64
		var failSummary, headerErrorKind, headerErrorCode, headerTraceID, planType sql.NullString

		if err := rows.Scan(
			&r.ID, &r.Provider, &r.BaseURL, &r.ExecutorType, &r.Model, &r.Alias,
			&r.APIKey, &r.SessionID, &r.ParentSessionID, &r.AuthID, &r.AuthIndex,
			&r.AuthType, &r.Source, &r.ReasoningEffort, &r.ServiceTier, &genInt,
			&reqAtStr, &r.LatencyMs, &r.TTFTMs, &failedInt,
			&r.FailureStatusCode, &r.FailureBody, &failSummary, &headerErrorKind, &headerErrorCode,
			&headerTraceID, &r.HeaderQuotaRecoverAtMS, &quotaUsedPct,
			&r.HeaderSecondaryQuotaRecoverAtMS, &secQuotaUsedPct, &planType,
			&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens,
			&r.CachedTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.TotalTokens,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost,
			&r.MatchedModel,
		); err != nil {
			return nil, err
		}

		r.Generate = genInt == 1
		r.Failed = failedInt == 1
		if failSummary.Valid {
			r.FailSummary = failSummary.String
		}
		if headerErrorKind.Valid {
			r.HeaderErrorKind = headerErrorKind.String
		}
		if headerErrorCode.Valid {
			r.HeaderErrorCode = headerErrorCode.String
		}
		if headerTraceID.Valid {
			r.HeaderTraceID = headerTraceID.String
		}
		if quotaUsedPct.Valid {
			r.HeaderQuotaUsedPercent = &quotaUsedPct.Float64
		}
		if secQuotaUsedPct.Valid {
			r.HeaderSecondaryQuotaUsedPercent = &secQuotaUsedPct.Float64
		}
		if planType.Valid {
			r.PlanType = planType.String
		}
		if t, err := time.Parse("2006-01-02 15:04:05", reqAtStr); err == nil {
			r.RequestedAt = t
		}

		records[r.AuthID] = r
	}

	return records, rows.Err()
}

func isAuthFailure(statusCode int, errorKind, errorCode, summary string) bool {
	if statusCode == 401 || statusCode == 403 {
		return true
	}
	if errorKind == "authentication" {
		return true
	}
	text := strings.ToLower(errorCode + " " + summary)
	authPatterns := []string{
		"invalid_grant",
		"invalid_api_key",
		"invalid_token",
		"token_expired",
		"revoked",
		"unauthorized",
		"unauthenticated",
		"bad_credentials",
		"no_auth_context",
		"authentication_failed",
		"reauth",
	}
	for _, p := range authPatterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func clampPercent(val *float64) *float64 {
	if val == nil {
		return nil
	}
	v := *val
	if v < 0 {
		v = 0
	}
	return &v
}
