package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Principal is the authenticated actor used for durable authorization. It is
// independent from MCP transport sessions and client vendor names.
type Principal struct {
	ID          string
	Kind        string
	SubjectHash string
}

// PrincipalFromCredentials maps already validated credentials to a stable,
// non-secret identifier. OAuth uses its validated issuer and subject; static
// bearer uses the token digest; open mode is local to this MCPX state store.
func PrincipalFromCredentials(credentials Credentials, authorizationHeader string) Principal {
	switch credentials.Source {
	case "oauth":
		subject := credentials.Issuer + "|" + credentials.Subject
		return hashedPrincipal("oauth", subject)
	case "static":
		return hashedPrincipal("bearer", bearerToken(authorizationHeader))
	default:
		return hashedPrincipal("local", "default")
	}
}

func hashedPrincipal(kind, subject string) Principal {
	subject = strings.TrimSpace(subject)
	sum := sha256.Sum256([]byte(subject))
	digest := hex.EncodeToString(sum[:])
	return Principal{ID: kind + ":" + digest[:24], Kind: kind, SubjectHash: digest}
}
