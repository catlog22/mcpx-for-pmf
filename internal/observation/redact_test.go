package observation

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeRedactsSensitiveKeysAndBoundsText(t *testing.T) {
	value := map[string]any{
		"token":  "secret-value",
		"nested": map[string]any{"password": "password-value"},
		"safe":   "visible",
	}
	clean, truncated := Sanitize(value, 256)
	if truncated {
		t.Fatal("small sanitized value must not be truncated")
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "password-value") {
		t.Fatalf("sensitive values leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), "visible") {
		t.Fatalf("safe value was removed: %s", encoded)
	}
}

func TestSanitizeTextKeepsUTF8ValidWhenTruncated(t *testing.T) {
	text, truncated := SanitizeText("前缀-模型输出-后缀", 12)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(text) || len(text) > 12 {
		t.Fatalf("text is not valid bounded UTF-8: %q", text)
	}
}

func TestSanitizeJSONReturnsValidBoundedJSON(t *testing.T) {
	clean, truncated := SanitizeJSON([]byte(`{"secret":"value","items":["one","two","three"]}`), 48)
	if !truncated {
		t.Fatal("expected bounded JSON to be truncated")
	}
	if len(clean) > 48 || !json.Valid(clean) {
		t.Fatalf("clean=%q len=%d", clean, len(clean))
	}
	if strings.Contains(string(clean), "value") {
		t.Fatalf("secret leaked: %s", clean)
	}
}
