package oauth

import (
	crand "crypto/rand"
	"encoding/base64"
)

// TokenURLSafe returns a URL-safe random string of roughly n bytes entropy.
func TokenURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
