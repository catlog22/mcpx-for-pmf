package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"regexp"
)

var (
	verifierRE  = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`)
	challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// ValidChallenge reports whether code_challenge looks like S256.
func ValidChallenge(challenge string) bool {
	return challengeRE.MatchString(challenge)
}

// VerifyS256 checks code_verifier against code_challenge (S256).
func VerifyS256(verifier, challenge string) bool {
	if !verifierRE.MatchString(verifier) || !ValidChallenge(challenge) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}
