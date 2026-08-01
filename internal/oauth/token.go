package oauth

import (
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	CodeTTLSeconds  = 300
	MaxPendingCodes = 256
	DefaultScope    = "mcp"
)

// Server holds process-local OAuth state.
type Server struct {
	Password    string
	ServerURL   string // configured origin; may be empty until request
	TokenSecret []byte
	TokenTTL    int // seconds
	Registry    *Registry

	mu    sync.Mutex
	codes map[string]*authCode
}

type authCode struct {
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
	ExpiresAt           time.Time
}

// NewServer builds an OAuth server. tokenSecret must be non-empty.
func NewServer(password, serverURL string, tokenSecret []byte, tokenTTL int) *Server {
	if tokenTTL <= 0 {
		tokenTTL = 86400
	}
	return &Server{
		Password:    password,
		ServerURL:   trimSlash(serverURL),
		TokenSecret: tokenSecret,
		TokenTTL:    tokenTTL,
		Registry:    NewRegistry(),
		codes:       map[string]*authCode{},
	}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// IssueCode creates a one-time authorization code after password approval.
func (s *Server) IssueCode(clientID, redirectURI, challenge, method, resource, scope string) (string, error) {
	if method != "" && method != "S256" {
		return "", fmt.Errorf("only S256 PKCE is supported")
	}
	if !ValidChallenge(challenge) {
		return "", fmt.Errorf("invalid code_challenge")
	}
	if !s.Registry.AcceptsRedirect(clientID, redirectURI) {
		return "", fmt.Errorf("invalid redirect_uri")
	}
	if scope == "" {
		scope = DefaultScope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneCodesLocked()
	if len(s.codes) >= MaxPendingCodes {
		return "", fmt.Errorf("too many pending authorization codes")
	}
	code := TokenURLSafe(32)
	s.codes[code] = &authCode{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Resource:            resource,
		Scope:               scope,
		ExpiresAt:           time.Now().Add(CodeTTLSeconds * time.Second),
	}
	return code, nil
}

// CheckPassword constant-time compares operator password.
func (s *Server) CheckPassword(got string) bool {
	return subtle.ConstantTimeCompare([]byte(s.Password), []byte(got)) == 1
}

// ExchangeCode validates code+PKCE and returns an access token JWT.
func (s *Server) ExchangeCode(code, redirectURI, clientID, codeVerifier, resource string) (string, int, error) {
	s.mu.Lock()
	ac, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	s.mu.Unlock()
	if !ok {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if time.Now().After(ac.ExpiresAt) {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if ac.ClientID != clientID || ac.RedirectURI != redirectURI {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if !VerifyS256(codeVerifier, ac.CodeChallenge) {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	res := ac.Resource
	if resource != "" {
		res = resource
	}
	issuer := s.EffectiveIssuer("")
	if issuer == "" {
		issuer = issuerFromResource(res)
	}
	if res == "" {
		res = s.ResourceURL(issuer)
	}
	if issuer == "" {
		return "", 0, fmt.Errorf("cannot determine token issuer; set auth.oauth.server_url")
	}
	tok, err := s.CreateAccessToken(clientID, res, issuer)
	if err != nil {
		return "", 0, err
	}
	return tok, s.TokenTTL, nil
}

func issuerFromResource(resource string) string {
	resource = trimSlash(resource)
	if strings.HasSuffix(resource, "/mcp") {
		return trimSlash(strings.TrimSuffix(resource, "/mcp"))
	}
	return resource
}

// CreateAccessToken issues HS256 JWT.
func (s *Server) CreateAccessToken(clientID, audience, issuer string) (string, error) {
	if len(s.TokenSecret) == 0 {
		return "", fmt.Errorf("token secret not configured")
	}
	if issuer == "" {
		issuer = s.EffectiveIssuer("")
	}
	if audience == "" {
		audience = s.ResourceURL(issuer)
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       issuer,
		"aud":       audience,
		"sub":       clientID,
		"client_id": clientID,
		"iat":       now.Unix(),
		"exp":       now.Add(time.Duration(s.TokenTTL) * time.Second).Unix(),
		"scope":     DefaultScope,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString(s.TokenSecret)
}

// ValidateAccessToken checks JWT signature, exp, iss, aud, and live client.
func (s *Server) ValidateAccessToken(token, issuer, audience string) bool {
	_, ok := s.ValidateAccessTokenIdentity(token, issuer, audience)
	return ok
}

// ValidateAccessTokenIdentity validates a token and returns its stable subject.
func (s *Server) ValidateAccessTokenIdentity(token, issuer, audience string) (string, bool) {
	if token == "" || len(s.TokenSecret) == 0 {
		return "", false
	}
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected alg")
		}
		return s.TokenSecret, nil
	}, jwt.WithAudience(audience), jwt.WithIssuer(issuer))
	if err != nil || !parsed.Valid {
		return "", false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}
	cid, _ := claims["client_id"].(string)
	if cid == "" {
		cid, _ = claims["sub"].(string)
	}
	if cid == "" {
		return "", false
	}
	_, exists := s.Registry.Get(cid)
	if !exists {
		return "", false
	}
	return cid, true
}

// EffectiveIssuer returns configured server URL or fallback origin.
func (s *Server) EffectiveIssuer(fallbackOrigin string) string {
	if s.ServerURL != "" {
		return s.ServerURL
	}
	return trimSlash(fallbackOrigin)
}

// ResourceURL is {issuer}/mcp.
func (s *Server) ResourceURL(issuer string) string {
	return trimSlash(issuer) + "/mcp"
}

func (s *Server) pruneCodesLocked() {
	now := time.Now()
	for k, v := range s.codes {
		if now.After(v.ExpiresAt) {
			delete(s.codes, k)
		}
	}
}
