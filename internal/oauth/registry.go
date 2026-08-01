package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	MaxRedirectURIs      = 10
	MaxRegisteredClients = 1024
)

// Client is a registered OAuth client.
type Client struct {
	ClientID                string
	RedirectURIs            []string
	TokenEndpointAuthMethod string
	ClientName              string
	SecretDigest            string // empty for public clients
	IssuedAt                int64
}

// Registry is an RFC7591 client store (memory + optional disk).
type Registry struct {
	mu          sync.RWMutex
	clients     map[string]*Client
	persistPath string // empty = memory only
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{clients: map[string]*Client{}}
}

// AddPreregistered installs a static client from config.
func (r *Registry) AddPreregistered(clientID string, redirectURIs []string, clientSecret string) error {
	uris, err := ValidateRedirectURIs(redirectURIs)
	if err != nil {
		return err
	}
	method := "none"
	digest := ""
	if clientSecret != "" {
		method = "client_secret_post"
		digest = secretDigest(clientSecret)
	}
	c := &Client{
		ClientID:                clientID,
		RedirectURIs:            uris,
		TokenEndpointAuthMethod: method,
		SecretDigest:            digest,
		IssuedAt:                time.Now().Unix(),
	}
	r.mu.Lock()
	r.clients[clientID] = c
	err = r.saveLocked()
	r.mu.Unlock()
	return err
}

// Register performs dynamic client registration.
func (r *Registry) Register(metadata map[string]any) (map[string]any, error) {
	rawURIs, _ := metadata["redirect_uris"].([]any)
	var uriStrs []string
	for _, u := range rawURIs {
		s, ok := u.(string)
		if !ok {
			return nil, fmt.Errorf("redirect_uris must be strings")
		}
		uriStrs = append(uriStrs, s)
	}
	// also accept []string via JSON re-decode path
	if len(uriStrs) == 0 {
		if ss, ok := metadata["redirect_uris"].([]string); ok {
			uriStrs = ss
		}
	}
	uris, err := ValidateRedirectURIs(uriStrs)
	if err != nil {
		return nil, err
	}

	method := "none"
	if v, ok := metadata["token_endpoint_auth_method"].(string); ok && v != "" {
		method = v
	}
	if method != "none" && method != "client_secret_post" && method != "client_secret_basic" {
		return nil, fmt.Errorf("unsupported token_endpoint_auth_method")
	}

	clientName := ""
	if v, ok := metadata["client_name"].(string); ok {
		clientName = strings.TrimSpace(v)
		if len(clientName) > 200 {
			clientName = clientName[:200]
		}
	}

	r.mu.Lock()
	if len(r.clients) >= MaxRegisteredClients {
		r.mu.Unlock()
		return nil, fmt.Errorf("dynamic client registration limit reached")
	}

	id := TokenURLSafe(24)
	for r.clients[id] != nil {
		id = TokenURLSafe(24)
	}
	var plainSecret string
	digest := ""
	if method != "none" {
		plainSecret = TokenURLSafe(32)
		digest = secretDigest(plainSecret)
	}
	c := &Client{
		ClientID:                id,
		RedirectURIs:            uris,
		TokenEndpointAuthMethod: method,
		ClientName:              clientName,
		SecretDigest:            digest,
		IssuedAt:                time.Now().Unix(),
	}
	r.clients[id] = c
	if err := r.saveLocked(); err != nil {
		delete(r.clients, id)
		r.mu.Unlock()
		return nil, fmt.Errorf("persist oauth client: %w", err)
	}
	r.mu.Unlock()

	out := map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        c.IssuedAt,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": c.TokenEndpointAuthMethod,
	}
	if c.ClientName != "" {
		out["client_name"] = c.ClientName
	}
	if plainSecret != "" {
		out["client_secret"] = plainSecret
		out["client_secret_expires_at"] = 0
	}
	return out, nil
}

// Get returns a client by id.
func (r *Registry) Get(clientID string) (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[clientID]
	return c, ok
}

// AcceptsRedirect checks exact redirect match.
func (r *Registry) AcceptsRedirect(clientID, redirectURI string) bool {
	c, ok := r.Get(clientID)
	if !ok {
		return false
	}
	for _, u := range c.RedirectURIs {
		if u == redirectURI {
			return true
		}
	}
	return false
}

// Authenticates verifies client credentials for the registered method.
func (r *Registry) Authenticates(clientID, clientSecret, authMethod string) bool {
	c, ok := r.Get(clientID)
	if !ok || c.TokenEndpointAuthMethod != authMethod {
		return false
	}
	if c.TokenEndpointAuthMethod == "none" {
		return clientSecret == ""
	}
	if c.SecretDigest == "" || clientSecret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.SecretDigest), []byte(secretDigest(clientSecret))) == 1
}

// ValidateRedirectURIs validates absolute redirect URIs.
func ValidateRedirectURIs(uris []string) ([]string, error) {
	if len(uris) == 0 || len(uris) > MaxRedirectURIs {
		return nil, fmt.Errorf("redirect_uris must contain between 1 and %d entries", MaxRedirectURIs)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(uris))
	for _, item := range uris {
		if item == "" || len(item) > 2048 {
			return nil, fmt.Errorf("invalid redirect_uri")
		}
		u, err := url.Parse(item)
		if err != nil || u.Fragment != "" || u.Scheme == "" || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("redirect_uri must be absolute without fragment or userinfo")
		}
		host := strings.ToLower(u.Hostname())
		switch u.Scheme {
		case "https":
		case "http":
			if host != "localhost" && host != "127.0.0.1" && host != "::1" {
				return nil, fmt.Errorf("HTTP redirect_uri only allowed for loopback")
			}
		default:
			return nil, fmt.Errorf("redirect_uri must use https or loopback http")
		}
		if _, ok := seen[item]; ok {
			return nil, fmt.Errorf("redirect_uris must be unique")
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func secretDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
