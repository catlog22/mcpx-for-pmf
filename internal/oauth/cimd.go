package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	cimdHTTPTimeout  = 10 * time.Second
	cimdCacheTTL     = 15 * time.Minute
	cimdMaxBody      = 1 << 20
	cimdMaxRedirects = 8
)

// ClientIDMetadataDocument is a subset of OAuth CIMD / RP metadata used by
// OpenAI ChatGPT connectors (client_id is an HTTPS URL).
type ClientIDMetadataDocument struct {
	ClientID                          string   `json:"client_id"`
	ClientName                        string   `json:"client_name"`
	RedirectURIs                      []string `json:"redirect_uris"`
	TokenEndpointAuthMethod           string   `json:"token_endpoint_auth_method"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	JWKSURI                           string   `json:"jwks_uri"`
}

type cimdCacheEntry struct {
	doc       *Client
	fetchedAt time.Time
}

// CIMDResolver fetches and caches Client ID Metadata Documents.
type CIMDResolver struct {
	mu     sync.RWMutex
	cache  map[string]cimdCacheEntry
	client *http.Client
	now    func() time.Time
}

// NewCIMDResolver builds a resolver with a bounded HTTP client.
func NewCIMDResolver() *CIMDResolver {
	return &CIMDResolver{
		cache: map[string]cimdCacheEntry{},
		client: &http.Client{
			Timeout: cimdHTTPTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= cimdMaxRedirects {
					return fmt.Errorf("too many redirects fetching CIMD")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("CIMD redirects must stay on https")
				}
				return nil
			},
		},
		now: time.Now,
	}
}

// IsClientIDMetadataURL reports whether client_id is an HTTPS metadata URL (CIMD).
func IsClientIDMetadataURL(clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 2048 {
		return false
	}
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	// Must look like an HTTP(S) URL path document, not a random opaque id.
	return true
}

// Resolve returns a Client view for a CIMD client_id URL.
func (r *CIMDResolver) Resolve(clientID string) (*Client, error) {
	if !IsClientIDMetadataURL(clientID) {
		return nil, fmt.Errorf("client_id is not a metadata document URL")
	}
	now := r.now()
	r.mu.RLock()
	if ent, ok := r.cache[clientID]; ok && now.Sub(ent.fetchedAt) < cimdCacheTTL {
		c := *ent.doc
		r.mu.RUnlock()
		return &c, nil
	}
	r.mu.RUnlock()

	doc, err := r.fetch(clientID)
	if err != nil {
		return nil, err
	}
	client, err := clientFromCIMD(clientID, doc)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[clientID] = cimdCacheEntry{doc: client, fetchedAt: now}
	r.mu.Unlock()
	out := *client
	return &out, nil
}

func (r *CIMDResolver) fetch(clientIDURL string) (*ClientIDMetadataDocument, error) {
	req, err := http.NewRequest(http.MethodGet, clientIDURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch CIMD: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch CIMD: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, cimdMaxBody))
	if err != nil {
		return nil, fmt.Errorf("read CIMD: %w", err)
	}
	var doc ClientIDMetadataDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse CIMD: %w", err)
	}
	return &doc, nil
}

func clientFromCIMD(clientID string, doc *ClientIDMetadataDocument) (*Client, error) {
	if doc == nil {
		return nil, fmt.Errorf("empty CIMD")
	}
	// client_id in the document should match the URL used as client_id (RFC CIMD).
	if doc.ClientID != "" && doc.ClientID != clientID {
		return nil, fmt.Errorf("CIMD client_id mismatch")
	}
	uris, err := ValidateRedirectURIs(doc.RedirectURIs)
	if err != nil {
		return nil, fmt.Errorf("CIMD redirect_uris: %w", err)
	}
	method := strings.TrimSpace(doc.TokenEndpointAuthMethod)
	if method == "" {
		// Prefer public-client when supported list includes none.
		for _, m := range doc.TokenEndpointAuthMethodsSupported {
			if m == "none" {
				method = "none"
				break
			}
		}
	}
	if method == "" {
		method = "none"
	}
	// We currently accept public CIMD clients (none). private_key_jwt is
	// advertised by ChatGPT but only selected when the AS also supports it.
	if method != "none" {
		// Still allow if supported list includes none (AS forces none).
		hasNone := false
		for _, m := range doc.TokenEndpointAuthMethodsSupported {
			if m == "none" {
				hasNone = true
				break
			}
		}
		if hasNone {
			method = "none"
		} else {
			return nil, fmt.Errorf("CIMD token_endpoint_auth_method %q not supported (need none)", method)
		}
	}
	return &Client{
		ClientID:                clientID,
		RedirectURIs:            uris,
		TokenEndpointAuthMethod: method,
		ClientName:              strings.TrimSpace(doc.ClientName),
		SecretDigest:            "",
		IssuedAt:                time.Now().Unix(),
	}, nil
}
