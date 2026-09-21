package plugin

import (
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

// HeaderDerived contains metrics extracted from upstream HTTP response headers.
type HeaderDerived struct {
	ResponseModel               string   `json:"response_model,omitempty"`
	QuotaRecoverAtMS            int64    `json:"quota_recover_at_ms,omitempty"`
	QuotaUsedPercent            *float64 `json:"quota_used_percent,omitempty"`
	QuotaWindowMinutes          *float64 `json:"quota_window_minutes,omitempty"`
	SecondaryQuotaRecoverAtMS   int64    `json:"secondary_quota_recover_at_ms,omitempty"`
	SecondaryQuotaUsedPercent   *float64 `json:"secondary_quota_used_percent,omitempty"`
	SecondaryQuotaWindowMinutes *float64 `json:"secondary_quota_window_minutes,omitempty"`
	RateLimitReachedType        string   `json:"rate_limit_reached_type,omitempty"`
	PlanType                    string   `json:"plan_type,omitempty"`
	ErrorKind                   string   `json:"error_kind,omitempty"`
	ErrorCode                   string   `json:"error_code,omitempty"`
	TraceID                     string   `json:"trace_id,omitempty"`
}

// ParseResponseHeaders extracts useful diagnostics from upstream response headers.
func ParseResponseHeaders(headers http.Header, statusCode int, baseTime time.Time) HeaderDerived {
	var derived HeaderDerived
	if baseTime.IsZero() {
		baseTime = time.Now()
	}

	// 1. Trace ID
	if headers != nil {
		// Response Model from headers (e.g. Openai-Model, X-Model, X-Response-Model, etc.)
		for _, key := range []string{
			"Openai-Model",
			"X-Model",
			"X-Response-Model",
			"X-Served-Model",
			"X-Amzn-Bedrock-Model-Id",
			"Model",
		} {
			if m := strings.TrimSpace(headers.Get(key)); m != "" {
				derived.ResponseModel = m
				break
			}
		}

		for _, key := range []string{
			"Cf-Ray", "X-Request-Id", "X-Amzn-Trace-Id", "Traceparent", "Zeabur-Request-Id",
		} {
			if v := strings.TrimSpace(headers.Get(key)); v != "" {
				derived.TraceID = v
				break
			}
		}

		// 2. Quota Recover At
		// Direct recover timestamp in ms
		for _, key := range []string{"X-Codex-Quota-Window-Recover-At-Ms", "X-Quota-Recover-At-Ms"} {
			if val := headers.Get(key); val != "" {
				if ms, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil && ms > 0 {
					derived.QuotaRecoverAtMS = ms
					break
				}
			}
		}

		// Retry-After header (seconds or HTTP date)
		if derived.QuotaRecoverAtMS == 0 {
			if retryAfter := strings.TrimSpace(headers.Get("Retry-After")); retryAfter != "" {
				if sec, err := strconv.ParseFloat(retryAfter, 64); err == nil && sec > 0 {
					derived.QuotaRecoverAtMS = baseTime.Add(time.Duration(sec * float64(time.Second))).UnixMilli()
				} else if t, err := mail.ParseDate(retryAfter); err == nil && !t.IsZero() {
					derived.QuotaRecoverAtMS = t.UnixMilli()
				}
			}
		}

		// RateLimit Reset
		if derived.QuotaRecoverAtMS == 0 {
			for _, key := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens", "X-Ratelimit-Reset"} {
				if resetStr := strings.TrimSpace(headers.Get(key)); resetStr != "" {
					if sec, err := strconv.ParseFloat(resetStr, 64); err == nil && sec > 0 {
						// could be unix timestamp or delta seconds
						if sec > 1_000_000_000 {
							derived.QuotaRecoverAtMS = int64(sec * 1000)
						} else {
							derived.QuotaRecoverAtMS = baseTime.Add(time.Duration(sec * float64(time.Second))).UnixMilli()
						}
						break
					} else if d, err := time.ParseDuration(resetStr); err == nil && d > 0 {
						derived.QuotaRecoverAtMS = baseTime.Add(d).UnixMilli()
						break
					}
				}
			}
		}

		// Quota percentage if present
		if pctStr := headers.Get("X-Quota-Used-Percent"); pctStr != "" {
			if p, err := strconv.ParseFloat(strings.TrimSpace(pctStr), 64); err == nil {
				derived.QuotaUsedPercent = &p
			}
		}

		// Error code from header.
		// Note: X-Should-Retry is deliberately excluded. It is a retry hint whose
		// value ("true"/"false") is not an error code, and upstreams send it on
		// successful responses too, which polluted diagnostics grouping.
		for _, key := range []string{"X-Error-Code", "X-Ide-Error-Code", "X-Openai-Ide-Error-Code"} {
			if code := strings.TrimSpace(headers.Get(key)); code != "" {
				derived.ErrorCode = code
				break
			}
		}

		// OpenAI / Codex Authorization Error
		if authErr := strings.TrimSpace(headers.Get("X-Openai-Authorization-Error")); authErr != "" {
			derived.ErrorKind = "authentication"
			derived.ErrorCode = authErr
		}

		// 3. Codex Quota Windows (Primary 5h & Secondary Weekly)
		if planType := strings.TrimSpace(headers.Get("X-Codex-Plan-Type")); planType != "" {
			derived.PlanType = strings.ToLower(planType)
		}

		// Primary window
		if pVal := strings.TrimSpace(headers.Get("X-Codex-Primary-Used-Percent")); pVal != "" {
			if p, err := strconv.ParseFloat(pVal, 64); err == nil {
				derived.QuotaUsedPercent = &p
			}
		}
		pReset := parseResetTime(headers.Get("X-Codex-Primary-Reset-At"), headers.Get("X-Codex-Primary-Reset-After-Seconds"), baseTime)
		if pReset > 0 {
			derived.QuotaRecoverAtMS = pReset
		}
		derived.QuotaWindowMinutes = parsePositiveHeaderFloat(headers.Get("X-Codex-Primary-Window-Minutes"))

		// Secondary window
		if sVal := strings.TrimSpace(headers.Get("X-Codex-Secondary-Used-Percent")); sVal != "" {
			if p, err := strconv.ParseFloat(sVal, 64); err == nil {
				derived.SecondaryQuotaUsedPercent = &p
			}
		}
		sReset := parseResetTime(headers.Get("X-Codex-Secondary-Reset-At"), headers.Get("X-Codex-Secondary-Reset-After-Seconds"), baseTime)
		if sReset > 0 {
			derived.SecondaryQuotaRecoverAtMS = sReset
		}
		derived.SecondaryQuotaWindowMinutes = parsePositiveHeaderFloat(headers.Get("X-Codex-Secondary-Window-Minutes"))

		// Rate limit reached window selection
		reachedType := strings.ToLower(strings.TrimSpace(headers.Get("X-Codex-Rate-Limit-Reached-Type")))
		derived.RateLimitReachedType = reachedType
		if reachedType == "primary" && pReset > 0 {
			derived.QuotaRecoverAtMS = pReset
			derived.ErrorKind = "rate_limit"
		} else if reachedType == "secondary" && sReset > 0 {
			derived.QuotaRecoverAtMS = sReset
			derived.ErrorKind = "rate_limit"
		}
	}

	// 4. Error Kind derivation based on status code and headers
	if statusCode >= 400 {
		switch statusCode {
		case 429:
			derived.ErrorKind = "rate_limit"
		case 401, 403:
			derived.ErrorKind = "authentication"
		case 404:
			derived.ErrorKind = "not_found"
		case 400, 422:
			derived.ErrorKind = "client_error"
		case 500, 502, 503, 504:
			derived.ErrorKind = "server_error"
		default:
			if derived.ErrorKind == "" {
				derived.ErrorKind = "http_error"
			}
		}
	}

	return derived
}

func parsePositiveHeaderFloat(value string) *float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil
	}
	return &parsed
}

func parseResetTime(resetAtStr, resetAfterStr string, baseTime time.Time) int64 {
	resetAfterStr = strings.TrimSpace(resetAfterStr)
	if resetAfterStr != "" {
		if sec, err := strconv.ParseFloat(resetAfterStr, 64); err == nil && sec > 0 {
			return baseTime.Add(time.Duration(sec * float64(time.Second))).UnixMilli()
		}
	}
	resetAtStr = strings.TrimSpace(resetAtStr)
	if resetAtStr != "" {
		if sec, err := strconv.ParseFloat(resetAtStr, 64); err == nil && sec > 0 {
			if sec > 1_000_000_000_000 {
				return int64(sec)
			} else if sec > 1_000_000_000 {
				return int64(sec * 1000)
			} else {
				return baseTime.Add(time.Duration(sec * float64(time.Second))).UnixMilli()
			}
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", time.RFC1123, time.RFC1123Z} {
			if t, err := time.Parse(layout, resetAtStr); err == nil && !t.IsZero() {
				return t.UnixMilli()
			}
		}
	}
	return 0
}
