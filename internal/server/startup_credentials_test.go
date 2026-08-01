package server

import (
	"bytes"
	"strings"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/logging"
)

func TestLogStartupCredentialsPrintsConfiguredValues(t *testing.T) {
	var output bytes.Buffer
	logging.Init(logging.Options{Level: "info", Format: "text", Out: &output})
	defer logging.Init(logging.Options{Level: "info"})

	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "dual"
	cfg.Auth.Token = "bearer-for-local-dev"
	cfg.Auth.OAuth.Password = "oauth-password-for-local-dev"
	logStartupCredentials(cfg, true, "v0.1.0-test")

	log := output.String()
	for _, value := range []string{"startup credentials", "version=v0.1.0-test", "bearer-for-local-dev", "oauth-password-for-local-dev"} {
		if !strings.Contains(log, value) {
			t.Fatalf("startup credential log missing %q: %s", value, log)
		}
	}
}
