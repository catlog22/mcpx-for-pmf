package server

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/config"
)

func TestBuildOAuthServerPersistsGeneratedTokenSecretAndDCRClients(t *testing.T) {
	t.Setenv("MCPX_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "oauth"
	cfg.Auth.OAuth.Password = "operator-password"
	cfg.Auth.OAuth.ServerURL = "https://mcp.example.com"

	first, err := buildOAuthServer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	redirect := "https://chatgpt.com/connector/oauth/reconnect-test"
	registered, err := first.Registry.Register(map[string]any{
		"client_name":                "ChatGPT",
		"redirect_uris":              []any{redirect},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	clientID, _ := registered["client_id"].(string)
	if clientID == "" {
		t.Fatal("missing client id")
	}

	second, err := buildOAuthServer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.TokenSecret) != string(second.TokenSecret) {
		t.Fatal("generated OAuth token secret changed across server initialization")
	}
	if !second.Registry.AcceptsRedirect(clientID, redirect) {
		t.Fatal("DCR client was not restored after server initialization")
	}
	secretPath := filepath.Join(os.Getenv("MCPX_HOME"), "oauth-token-secret")
	if info, err := os.Stat(secretPath); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token secret file is not private: info=%v err=%v", info, err)
	}
}

func TestBuildOAuthServerUsesConfiguredTokenSecret(t *testing.T) {
	t.Setenv("MCPX_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "oauth"
	cfg.Auth.OAuth.Password = "operator-password"
	cfg.Auth.OAuth.TokenSecret = strings.Repeat("ab", 32)

	server, err := buildOAuthServer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(server.TokenSecret); got != cfg.Auth.OAuth.TokenSecret {
		t.Fatalf("token secret = %q, want configured secret", got)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("MCPX_HOME"), "oauth-token-secret")); !os.IsNotExist(err) {
		t.Fatalf("automatic secret file should not be created, err=%v", err)
	}
}

func TestBuildOAuthServerRejectsInvalidPersistedTokenSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "oauth-token-secret"), []byte("not-hex"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "oauth"
	cfg.Auth.OAuth.Password = "operator-password"
	if _, err := buildOAuthServer(&cfg); err == nil {
		t.Fatal("expected invalid persisted token secret error")
	}
}
