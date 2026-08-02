package observation

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const redactedValue = "[REDACTED]"

var sensitiveKeyParts = []string{
	"token", "secret", "password", "authorization", "cookie", "api_key", "apikey", "private_key", "client_secret",
}

var sensitiveTextPattern = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;]+|((?:["']?\b(?:token|secret|password|authorization|cookie|api[_-]?key|private[_-]?key|client[_-]?secret)\b["']?\s*[:=]\s*["']?))[^"'\s,;}]+`)

// Sanitize recursively removes values under sensitive keys and bounds the
// resulting JSON representation without breaking UTF-8.
func Sanitize(value any, maxBytes int) (any, bool) {
	if maxBytes <= 0 {
		maxBytes = MaxEventBytes
	}
	clean := sanitizeValue(value)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return redactedValue, true
	}
	if len(encoded) <= maxBytes {
		return clean, false
	}
	for previewBytes := maxBytes; previewBytes > 0; previewBytes-- {
		preview := truncateUTF8(string(encoded), previewBytes)
		candidate := map[string]any{"truncated": true, "preview": preview}
		candidateBytes, marshalErr := json.Marshal(candidate)
		if marshalErr == nil && len(candidateBytes) <= maxBytes {
			return candidate, true
		}
	}
	return map[string]any{"truncated": true}, true
}

// SanitizeText applies the same UTF-8-safe bound to plain command output.
func SanitizeText(value string, maxBytes int) (string, bool) {
	value = RedactText(value)
	if maxBytes <= 0 {
		maxBytes = MaxEventBytes
	}
	if len(value) <= maxBytes {
		return value, false
	}
	suffix := "\n… [truncated]"
	if maxBytes <= len(suffix) {
		return truncateUTF8(suffix, maxBytes), true
	}
	return truncateUTF8(value, maxBytes-len(suffix)) + suffix, true
}

// RedactText removes common inline credential forms from human-readable
// intent, summaries and command output. Structured JSON uses key-based
// redaction in Sanitize; this covers text fields that have no keys.
func RedactText(value string) string {
	return sensitiveTextPattern.ReplaceAllString(value, "$1$2[REDACTED]")
}

// SanitizeIntent applies text redaction and the protocol intent limit.
func SanitizeIntent(value string) string {
	clean, _ := SanitizeText(strings.TrimSpace(value), MaxIntentBytes)
	return clean
}

// SanitizeJSON parses, sanitizes and bounds an already encoded JSON value.
func SanitizeJSON(value []byte, maxBytes int) ([]byte, bool) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		text, truncated := SanitizeText(string(value), maxBytes)
		encoded, _ := json.Marshal(text)
		return encoded, truncated
	}
	clean, truncated := Sanitize(decoded, maxBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return []byte(`"[REDACTED]"`), true
	}
	return encoded, truncated
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				clean[key] = redactedValue
				continue
			}
			clean[key] = sanitizeValue(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, item := range typed {
			clean[i] = sanitizeValue(item)
		}
		return clean
	case string:
		return RedactText(typed)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(key))
	for _, part := range sensitiveKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
