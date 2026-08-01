package auth

import (
	"strings"

	"mcpx/internal/config"
	"mcpx/internal/oauth"
)

// Credentials holds resolved auth material for a request.
type Credentials struct {
	OK      bool
	Source  string // "open" | "static" | "oauth"
	Issuer  string
	Subject string
}

// ValidateHTTP checks Authorization against auth mode.
// staticToken is the effective static bearer (global or project).
// oauthServer may be nil when mode is open/bearer-only.
func ValidateHTTP(header string, mode string, staticToken string, oauthServer *oauth.Server, issuer, resource string) Credentials {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = config.EffectiveAuthMode(config.AuthConfig{Token: staticToken})
	}
	switch mode {
	case "open":
		return Credentials{OK: true, Source: "open"}
	case "bearer":
		if staticToken == "" {
			// misconfigured: require token but empty => deny
			return Credentials{OK: false}
		}
		if CheckBearer(header, staticToken) {
			return Credentials{OK: true, Source: "static"}
		}
		return Credentials{OK: false}
	case "oauth":
		return validateOAuth(header, oauthServer, issuer, resource)
	case "dual":
		if staticToken != "" && CheckBearer(header, staticToken) {
			return Credentials{OK: true, Source: "static"}
		}
		return validateOAuth(header, oauthServer, issuer, resource)
	default:
		return Credentials{OK: false}
	}
}

func validateOAuth(header string, s *oauth.Server, issuer, resource string) Credentials {
	if s == nil {
		return Credentials{OK: false}
	}
	tok := bearerToken(header)
	if tok == "" {
		return Credentials{OK: false}
	}
	subject, ok := s.ValidateAccessTokenIdentity(tok, issuer, resource)
	if ok {
		return Credentials{OK: true, Source: "oauth", Issuer: issuer, Subject: subject}
	}
	return Credentials{OK: false}
}

func bearerToken(header string) string {
	h := strings.TrimSpace(header)
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// CheckBearerStrict requires non-empty required token (unlike CheckBearer which allows empty=ok).
func CheckBearerStrict(authorizationHeader, required string) bool {
	if required == "" {
		return false
	}
	return CheckBearer(authorizationHeader, required)
}
