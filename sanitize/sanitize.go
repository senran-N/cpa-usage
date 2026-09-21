package sanitize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFailSummaryBytes = 1024
	quotedKeyPattern    = `(?:"([^"\r\n]+)"|'([^'\r\n]+)')`
	unquotedKeyPattern  = `\b([a-zA-Z0-9_.-]+(?:[ \t]+[a-zA-Z0-9_.-]+){0,2})\b`
)

var (
	// authColonHeaderRegex matches "Authorization: ..." or "*_authorization: ..." colon headers
	authColonHeaderRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*:\s*)[^\r\n]+`)

	// authSimpleAssignmentRegex matches "Authorization=Basic <token>" or "Authorization=Bearer <token>"
	authSimpleAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*=\s*)(?:basic|bearer)\s+[A-Za-z0-9._~+/=-]+`)

	// authComplexAssignmentRegex matches unquoted non-Basic/Bearer authorization assignments
	authComplexAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*=\s*)[a-zA-Z][^\r\n]*`)

	// cookieColonHeaderRegex matches "Cookie: ..." or "*_cookie: ..." colon headers
	cookieColonHeaderRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*cookie)(\s*:\s*)[^\r\n]+`)

	// cookieAssignmentRegex matches unquoted cookie assignments
	cookieAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*cookie)(\s*=\s*)([^"'\r\n][^\r\n]*)`)

	// bearerTokenRegex matches standalone Bearer tokens
	bearerTokenRegex = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)

	// pemPrivateKeyBlockRegex matches PEM private key blocks
	pemPrivateKeyBlockRegex = regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED |DSA )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |ENCRYPTED |DSA )?PRIVATE KEY-----`)

	// Quoted key matchers
	quotedKeyDoubleQuotedRegex       = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\]|\\.)*)"`)
	quotedKeySingleQuotedRegex       = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\]|\\.)*)'`)
	quotedKeyUnterminatedDoubleRegex = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	quotedKeyUnterminatedSingleRegex = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	quotedKeyUnquotedRegex           = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)

	// Unquoted key matchers
	unquotedKeyDoubleQuotedRegex       = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\]|\\.)*)"`)
	unquotedKeySingleQuotedRegex       = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\]|\\.)*)'`)
	unquotedKeyUnterminatedDoubleRegex = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	unquotedKeyUnterminatedSingleRegex = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	unquotedKeyUnquotedEqualsRegex     = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*=\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)
	unquotedKeyUnquotedColonRegex      = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*:\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)

	// strongTokenRegex matches authentic tokens
	strongTokenRegex = regexp.MustCompile(`(?i)\b(sk-proj-[A-Za-z0-9_-]{24,}|sk-ant-[A-Za-z0-9_-]{24,}|sk-[A-Za-z0-9]{24,}|github_pat_[A-Za-z0-9_]{40,}|ghp_[A-Za-z0-9]{30,}|AIza[0-9A-Za-z_-]{30,}|hf_[A-Za-z0-9]{30,}|sess-[A-Za-z0-9_-]{24,}|pk_(?:live|test)_[0-9a-zA-Z]{24,}|pk_[0-9a-zA-Z]{24,}|rk_(?:live|test)_[0-9a-zA-Z]{24,}|rk_[0-9a-zA-Z]{24,}|cpamp_[A-Za-z0-9_-]{32,})\b`)

	// emailRegex matches email patterns for masking
	emailRegex = regexp.MustCompile(`(?i)\b([a-zA-Z0-9_.+-])[a-zA-Z0-9_.+-]*(@[a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)\b`)
)

var secretKeySuffixes = []string{
	"_api_key",
	"_management_key",
	"_access_token",
	"_refresh_token",
	"_id_token",
	"_auth_token",
	"_session_token",
	"_client_secret",
	"_private_key",
	"_password",
	"_passwd",
	"_secret",
	"_authorization",
	"_cookie",
}

var secretExactKeys = map[string]bool{
	"api_key":            true,
	"apikey":             true,
	"x_api_key":          true,
	"xapi_key":           true,
	"xapikey":            true,
	"management_key":     true,
	"managementkey":      true,
	"cpa_management_key": true,
	"cpamanagementkey":   true,
	"authorization":      true,
	"cookie":             true,
	"set_cookie":         true,
	"access_token":       true,
	"refresh_token":      true,
	"id_token":           true,
	"token":              true,
	"client_secret":      true,
	"clientsecret":       true,
	"private_key":        true,
	"privatekey":         true,
	"secret":             true,
	"password":           true,
	"passwd":             true,
	"auth_token":         true,
	"authtoken":          true,
	"session":            true,
	"session_token":      true,
	"sessiontoken":       true,
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func normalizeSecretKey(key string) string {
	var builder strings.Builder
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			builder.WriteRune(unicode.ToLower(r))
		} else if r == '-' || r == ' ' || r == '.' {
			builder.WriteRune('_')
		} else {
			builder.WriteRune(r)
		}
	}
	return strings.Trim(builder.String(), "_")
}

func isSecretFieldKey(key string) bool {
	norm := normalizeSecretKey(key)
	if norm == "" {
		return false
	}
	if secretExactKeys[norm] {
		return true
	}
	for _, suffix := range secretKeySuffixes {
		if strings.HasSuffix(norm, suffix) {
			return true
		}
	}
	return false
}

func matchQuotedKeyCandidate(sub []string) (string, bool) {
	if len(sub) < 4 {
		return "", false
	}
	if sub[1] != "" && isSecretFieldKey(sub[1]) {
		return sub[1], true
	}
	if sub[2] != "" && isSecretFieldKey(sub[2]) {
		return sub[2], true
	}
	return "", false
}

func matchUnquotedKeyCandidate(sub []string) (nonSecretPrefix string, secretKey string, ok bool) {
	if len(sub) < 2 {
		return "", "", false
	}
	rawKey := sub[1]
	if rawKey == "" {
		return "", "", false
	}
	if isSecretFieldKey(rawKey) {
		return "", rawKey, true
	}
	words := strings.Fields(rawKey)
	if len(words) > 1 {
		for i := 1; i < len(words); i++ {
			candidate := strings.Join(words[i:], " ")
			if isSecretFieldKey(candidate) {
				norm := normalizeSecretKey(candidate)
				if norm == "token" {
					continue
				}
				idx := strings.Index(rawKey, candidate)
				var p string
				if idx > 0 {
					p = rawKey[:idx]
				}
				return p, candidate, true
			}
		}
	}
	return "", "", false
}

func sanitizeQuotedKeyAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		_, ok := matchQuotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[3]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		return fullPrefix + quote + "[redacted]" + quote
	})
}

func sanitizeQuotedKeyUnterminatedAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		_, ok := matchQuotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[3]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		trailing := ""
		if len(sub) > 5 {
			trailing = sub[5]
		}
		return fullPrefix + quote + "[redacted]" + trailing
	})
}

func sanitizeUnquotedKeyAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		prefix, secretKey, ok := matchUnquotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[2]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		if prefix != "" {
			keyIdx := strings.Index(fullPrefix, secretKey)
			if keyIdx > 0 {
				return fullPrefix[:keyIdx] + secretKey + sep + quote + "[redacted]" + quote
			}
		}
		return fullPrefix + quote + "[redacted]" + quote
	})
}

// ContainsCredential reports whether value contains credentials.
func ContainsCredential(value string) bool {
	return strongTokenRegex.MatchString(value) ||
		bearerTokenRegex.MatchString(value) ||
		authColonHeaderRegex.MatchString(value) ||
		authSimpleAssignmentRegex.MatchString(value) ||
		cookieColonHeaderRegex.MatchString(value) ||
		pemPrivateKeyBlockRegex.MatchString(value)
}

// SanitizeCredentialText scrubs credentials from arbitrary text or malformed JSON payloads.
func SanitizeCredentialText(value string) string {
	if value == "" {
		return ""
	}
	res := value

	// Headers & Tokens
	res = authColonHeaderRegex.ReplaceAllString(res, `${1}${2}[redacted]`)
	res = authSimpleAssignmentRegex.ReplaceAllString(res, `${1}${2}[redacted]`)
	res = authComplexAssignmentRegex.ReplaceAllString(res, `${1}${2}[redacted]`)
	res = cookieColonHeaderRegex.ReplaceAllString(res, `${1}${2}[redacted]`)
	res = cookieAssignmentRegex.ReplaceAllString(res, `${1}${2}[redacted]`)
	res = pemPrivateKeyBlockRegex.ReplaceAllString(res, `[redacted]`)
	res = bearerTokenRegex.ReplaceAllString(res, `Bearer [redacted]`)

	// Quoted key assignments
	res = sanitizeQuotedKeyAssignments(res, quotedKeyDoubleQuotedRegex, "\"")
	res = sanitizeQuotedKeyAssignments(res, quotedKeySingleQuotedRegex, "'")
	res = sanitizeQuotedKeyUnterminatedAssignments(res, quotedKeyUnterminatedDoubleRegex, "\"")
	res = sanitizeQuotedKeyUnterminatedAssignments(res, quotedKeyUnterminatedSingleRegex, "'")
	res = sanitizeQuotedKeyAssignments(res, quotedKeyUnquotedRegex, "")

	// Unquoted key assignments
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyDoubleQuotedRegex, "\"")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeySingleQuotedRegex, "'")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyUnquotedEqualsRegex, "")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyUnquotedColonRegex, "")

	res = strongTokenRegex.ReplaceAllString(res, `[redacted]`)
	return res
}

// SanitizeDiagnosticBody sanitizes failure bodies without truncating.
// If valid JSON, it traverses the JSON structure redacting secret keys/values.
// If not JSON, it falls back to SanitizeCredentialText.
func SanitizeDiagnosticBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err == nil {
		var trailing any
		if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
			sanitized := sanitizeDiagnosticJSONValue(payload)
			if out, err := json.Marshal(sanitized); err == nil {
				return string(out)
			}
		}
	}
	return SanitizeCredentialText(trimmed)
}

func sanitizeDiagnosticJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			sanitizedKey := key
			if ContainsCredential(key) {
				sanitizedKey = "[redacted-key:" + sha256Hex(key) + "]"
			}
			if isSecretFieldKey(key) {
				result[sanitizedKey] = "[redacted]"
				continue
			}
			result[sanitizedKey] = sanitizeDiagnosticJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = sanitizeDiagnosticJSONValue(child)
		}
		return result
	case string:
		return SanitizeCredentialText(v)
	case json.Number, bool, nil:
		return v
	default:
		return v
	}
}

// FailSummaryFromBody extracts a concise, sanitized diagnostic summary from a failure body.
func FailSummaryFromBody(body string) string {
	summary := strings.TrimSpace(body)
	if summary == "" {
		return ""
	}
	summary = SanitizeCredentialText(summary)
	summary = emailRegex.ReplaceAllString(summary, `${1}***${2}`)

	// If it's JSON with "error" -> "message", extract that directly
	var obj map[string]any
	if err := json.Unmarshal([]byte(summary), &obj); err == nil {
		if errVal, ok := obj["error"]; ok {
			switch e := errVal.(type) {
			case string:
				summary = e
			case map[string]any:
				if msg, ok := e["message"].(string); ok && msg != "" {
					summary = msg
				}
			}
		} else if msg, ok := obj["message"].(string); ok && msg != "" {
			summary = msg
		}
	}

	return truncateUTF8Bytes(strings.TrimSpace(summary), maxFailSummaryBytes)
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	limit := maxBytes
	suffix := ""
	if maxBytes > 3 {
		limit = maxBytes - 3
		suffix = "..."
	}
	var builder strings.Builder
	for _, r := range value {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if builder.Len()+size > limit {
			break
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String()) + suffix
}
