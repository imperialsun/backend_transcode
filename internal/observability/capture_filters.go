package observability

import "strings"

// ShouldSkipObservabilityCaptureRoute reports whether a route should stay out of
// persisted backend error/performance tables.
func ShouldSkipObservabilityCaptureRoute(route string) bool {
	normalized := strings.TrimSpace(route)
	if normalized == "" {
		return false
	}

	normalized = strings.TrimRight(normalized, "/")
	if normalized == "" {
		normalized = "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}

	if normalized == "/refresh" {
		return true
	}
	return strings.HasSuffix(normalized, "/auth/refresh")
}
