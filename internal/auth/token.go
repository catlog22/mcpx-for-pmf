package auth

import (
	"strings"
)

// CheckBearer validates Authorization header against required token.
// required empty => always ok.
func CheckBearer(authorizationHeader, required string) bool {
	if required == "" {
		return true
	}
	const prefix = "Bearer "
	h := strings.TrimSpace(authorizationHeader)
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return tok == required
}
