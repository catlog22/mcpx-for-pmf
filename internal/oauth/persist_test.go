package oauth

import (
	"path/filepath"
	"testing"
)

func TestRegistryPersistReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth-clients.json")

	r1 := NewRegistry()
	if err := r1.SetPersistPath(path); err != nil {
		t.Fatal(err)
	}
	out, err := r1.Register(map[string]any{
		"redirect_uris":              []any{"https://chatgpt.com/connector/oauth/abc"},
		"token_endpoint_auth_method": "none",
		"client_name":                "ChatGPT",
	})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := out["client_id"].(string)
	if cid == "" {
		t.Fatal("no client_id")
	}

	r2 := NewRegistry()
	if err := r2.SetPersistPath(path); err != nil {
		t.Fatal(err)
	}
	if !r2.AcceptsRedirect(cid, "https://chatgpt.com/connector/oauth/abc") {
		t.Fatal("persisted client not loaded")
	}
	c, ok := r2.Get(cid)
	if !ok || c.ClientName != "ChatGPT" {
		t.Fatalf("got %+v", c)
	}
}
