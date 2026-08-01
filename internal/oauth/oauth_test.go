package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestPKCEAndTokenRoundTrip(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	s := NewServer("op-pass", "https://mcp.example.com", secret, 3600)
	if err := s.Registry.AddPreregistered("cli", []string{"http://127.0.0.1/cb"}, ""); err != nil {
		t.Fatal(err)
	}
	verifier := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
	if len(verifier) < 43 {
		t.Fatal("verifier too short")
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !VerifyS256(verifier, challenge) {
		t.Fatal("pkce")
	}
	code, err := s.IssueCode("cli", "http://127.0.0.1/cb", challenge, "S256", "https://mcp.example.com/mcp", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	tok, ttl, err := s.ExchangeCode(code, "http://127.0.0.1/cb", "cli", verifier, "")
	if err != nil || ttl <= 0 || tok == "" {
		t.Fatalf("exchange: %v ttl=%d", err, ttl)
	}
	if !s.ValidateAccessToken(tok, "https://mcp.example.com", "https://mcp.example.com/mcp") {
		t.Fatal("validate")
	}
	// replay code
	if _, _, err := s.ExchangeCode(code, "http://127.0.0.1/cb", "cli", verifier, ""); err == nil {
		t.Fatal("expected replay fail")
	}
}

func TestRedirectRejectsPublicHTTP(t *testing.T) {
	_, err := ValidateRedirectURIs([]string{"http://evil.example/cb"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDCR(t *testing.T) {
	r := NewRegistry()
	out, err := r.Register(map[string]any{
		"redirect_uris":              []any{"https://app.example/callback"},
		"token_endpoint_auth_method": "none",
		"client_name":                "Web Chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out["client_id"].(string)
	if id == "" {
		t.Fatal("no client_id")
	}
	if !r.AcceptsRedirect(id, "https://app.example/callback") {
		t.Fatal("redirect")
	}
}

func TestCheckPassword(t *testing.T) {
	s := NewServer("secret", "", make([]byte, 32), 60)
	if !s.CheckPassword("secret") || s.CheckPassword("wrong") {
		t.Fatal("password")
	}
}
