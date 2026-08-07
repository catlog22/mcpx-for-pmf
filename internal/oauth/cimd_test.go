package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsClientIDMetadataURL(t *testing.T) {
	if !IsClientIDMetadataURL("https://chatgpt.com/oauth/client.json") {
		t.Fatal("expected CIMD URL")
	}
	if IsClientIDMetadataURL("MQ9QcdJ86IhTaisYl6lADFwVJ3E2xiwl") {
		t.Fatal("opaque id must not be CIMD")
	}
	if IsClientIDMetadataURL("http://insecure.example/client.json") {
		t.Fatal("http must be rejected")
	}
}

func TestCIMDResolveAndRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/client.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                             "https://example.test/oauth/client.json",
			"client_name":                           "ChatGPT",
			"redirect_uris":                         []string{"https://chatgpt.com/connector_platform_oauth_redirect"},
			"token_endpoint_auth_method":            "private_key_jwt",
			"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		})
	})
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()

	// Rewrite client_id host to test server — use HTTP client that trusts test cert via custom transport.
	// Simpler path: inject document via Resolve using a custom client on CIMDResolver.
	r := NewCIMDResolver()
	r.client = ts.Client()

	// Document self client_id must match request URL. Build document URL on ts.
	docURL := ts.URL + "/oauth/client.json"
	// Server returns fixed client_id example.test — adjust handler to echo request URL.
	mux.HandleFunc("/oauth/echo.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":   docURL,
			"client_name": "ChatGPT",
			"redirect_uris": []string{
				"https://chatgpt.com/connector_platform_oauth_redirect",
			},
			"token_endpoint_auth_method":            "private_key_jwt",
			"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		})
	})
	// Fix: docURL for echo path
	docURL = ts.URL + "/oauth/echo.json"
	// re-register with correct client_id in body — update handler
	mux.HandleFunc("/oauth/ok.json", func(w http.ResponseWriter, req *http.Request) {
		cid := "https://" + req.Host + "/oauth/ok.json"
		// httptest TLS server Host may not include scheme; use full URL from test
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":   ts.URL + "/oauth/ok.json",
			"client_name": "ChatGPT",
			"redirect_uris": []string{
				"https://chatgpt.com/connector_platform_oauth_redirect",
			},
			"token_endpoint_auth_method":            "private_key_jwt",
			"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		})
		_ = cid
	})

	clientID := ts.URL + "/oauth/ok.json"
	c, err := r.Resolve(clientID)
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenEndpointAuthMethod != "none" {
		t.Fatalf("expected none for AS compatibility, got %q", c.TokenEndpointAuthMethod)
	}

	s := NewServer("pw", "https://mcp.example.com", make([]byte, 32), 60)
	s.CIMD = r
	if !s.AcceptsClientRedirect(clientID, "https://chatgpt.com/connector_platform_oauth_redirect") {
		t.Fatal("legacy redirect")
	}
	if !s.AcceptsClientRedirect(clientID, "https://chatgpt.com/connector/oauth/abc123") {
		t.Fatal("path-scoped connector redirect should be allowed for ChatGPT CIMD")
	}
	if s.AcceptsClientRedirect(clientID, "https://evil.example/cb") {
		t.Fatal("foreign redirect must fail")
	}
	if !s.AuthenticatesClient(clientID, "", "none") {
		t.Fatal("public CIMD client auth")
	}
}

func TestAuthorizationServerMetadataAdvertisesCIMD(t *testing.T) {
	secret := make([]byte, 32)
	s := NewServer("pw", "https://mcp.example.com", secret, 60)
	h := &Handler{S: s}
	req := httptest.NewRequest(http.MethodGet, "https://mcp.example.com/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()
	h.HandleAuthorizationServerMetadata(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["client_id_metadata_document_supported"] != true {
		t.Fatalf("CIMD flag missing: %+v", body)
	}
}
