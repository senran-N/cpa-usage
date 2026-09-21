package sanitize

import (
	"strings"
	"testing"
)

func TestSanitizeCredentialText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		redacted []string
	}{
		{
			name:     "API key in error text",
			input:    "upstream error: failed with key sk-proj-1234567890abcdef1234567890abcdef and status 401",
			contains: []string{"upstream error: failed with key", "status 401"},
			redacted: []string{"sk-proj-1234567890abcdef1234567890abcdef"},
		},
		{
			name:     "Authorization header",
			input:    "Authorization: Bearer ya29.a0AfH6SMAxyz1234567890",
			contains: []string{"Authorization: [redacted]"},
			redacted: []string{"ya29.a0AfH6SMAxyz1234567890"},
		},
		{
			name:     "Standalone Bearer token in log text",
			input:    "Request failed with Bearer ya29.a0AfH6SMAxyz1234567890 during execution",
			contains: []string{"Bearer [redacted]"},
			redacted: []string{"ya29.a0AfH6SMAxyz1234567890"},
		},
		{
			name:     "PEM private key",
			input:    "error: -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...\n-----END RSA PRIVATE KEY----- failed",
			contains: []string{"[redacted]", "failed"},
			redacted: []string{"MIIEowIBAAKCAQEA0"},
		},
		{
			name:     "JSON assignment of api_key",
			input:    `{"api_key":"secret-value-123","model":"gpt-4"}`,
			contains: []string{`"api_key":"[redacted]"`, `"gpt-4"`},
			redacted: []string{"secret-value-123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeCredentialText(tt.input)
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("expected %q to contain %q", got, c)
				}
			}
			for _, r := range tt.redacted {
				if strings.Contains(got, r) {
					t.Errorf("expected %q to NOT contain %q", got, r)
				}
			}
		})
	}
}

func TestSanitizeDiagnosticBody(t *testing.T) {
	rawJSON := `{"error":{"message":"Invalid token: sk-ant-1234567890abcdef1234567890abcdef","type":"invalid_request_error","api_key":"my-secret-key"}}`
	sanitized := SanitizeDiagnosticBody(rawJSON)

	if strings.Contains(sanitized, "sk-ant-1234567890abcdef1234567890abcdef") {
		t.Errorf("secret token was not redacted: %s", sanitized)
	}
	if strings.Contains(sanitized, "my-secret-key") {
		t.Errorf("api_key was not redacted: %s", sanitized)
	}
	if !strings.Contains(sanitized, "invalid_request_error") {
		t.Errorf("non-sensitive structure was lost: %s", sanitized)
	}
}

func TestFailSummaryFromBody(t *testing.T) {
	raw := `{"error":{"message":"Rate limit exceeded for model gpt-4o, retry after 20s","type":"rate_limit"}}`
	summary := FailSummaryFromBody(raw)
	if summary != "Rate limit exceeded for model gpt-4o, retry after 20s" {
		t.Errorf("unexpected summary: %q", summary)
	}
}
