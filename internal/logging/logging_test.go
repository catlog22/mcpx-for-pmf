package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestInitText(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: "text", Out: &buf})
	Info("hello", "component", "test", "n", 1)
	s := buf.String()
	if !strings.Contains(s, "hello") || !strings.Contains(s, "component=test") {
		t.Fatalf("log line: %q", s)
	}
}

func TestInitJSON(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: "json", Out: &buf})
	Info("world", "component", "test")
	s := buf.String()
	if !strings.Contains(s, `"msg":"world"`) || !strings.Contains(s, `"component":"test"`) {
		t.Fatalf("json line: %q", s)
	}
}
