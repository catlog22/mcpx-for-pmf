package server

import "unicode/utf8"

// TruncateUTF8 truncates s to at most maxBytes of valid UTF-8.
func TruncateUTF8(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	// walk back to rune boundary
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	if maxBytes <= 0 {
		return "", true
	}
	return s[:maxBytes], true
}
