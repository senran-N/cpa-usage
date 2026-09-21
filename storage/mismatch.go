package storage

import "strings"

// cleanModelPrefix removes typical vendor namespace prefixes.
func cleanModelPrefix(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	for _, prefix := range []string{"models/", "openai/", "anthropic/", "google/", "devin/", "meta/", "xai/", "deepseek/"} {
		if strings.HasPrefix(m, prefix) {
			m = strings.TrimPrefix(m, prefix)
		}
	}
	return m
}

// DetectModelMismatch evaluates whether an upstream response model deviates significantly
// from the requested model or the resolved routing model (e.g. stealth downgrades to mini/haiku/flash).
func DetectModelMismatch(requestedModel, resolvedModel, responseModel string) bool {
	resp := cleanModelPrefix(responseModel)
	if resp == "" {
		return false
	}
	req := cleanModelPrefix(requestedModel)
	res := cleanModelPrefix(resolvedModel)

	// If response model matches either requested or resolved, it's consistent.
	if (req != "" && resp == req) || (res != "" && resp == res) {
		return false
	}

	// Keywords indicating distinct performance tiers or variants
	tierKeywords := []string{"mini", "haiku", "flash", "nano", "lite", "chat", "small", "micro"}

	hasTierConflict := func(target string) bool {
		if target == "" {
			return false
		}
		for _, kw := range tierKeywords {
			targetHas := strings.Contains(target, kw)
			respHas := strings.Contains(resp, kw)
			if respHas != targetHas {
				return true
			}
		}
		return false
	}

	// Check if response is a dated snapshot of requested or resolved (e.g. gpt-4o vs gpt-4o-2024-08-06)
	isDatedSnapshot := func(target string) bool {
		if target == "" {
			return false
		}
		if hasTierConflict(target) {
			return false
		}
		baseTarget := strings.TrimSuffix(target, "-latest")
		if strings.HasPrefix(resp, target+"-") || strings.HasPrefix(resp, baseTarget+"-") {
			return true
		}
		return false
	}

	if (req != "" && isDatedSnapshot(req)) || (res != "" && isDatedSnapshot(res)) {
		return false
	}

	// Otherwise, it is a mismatch!
	return true
}
