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

func TestRedactTextRedactsCredentialHeadersAndAssignments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "authorization bearer header", input: "Authorization: Bearer header-secret", want: "Authorization: Bearer [REDACTED]"},
		{name: "password assignment", input: "password=plain-password", want: "password=[REDACTED]"},
		{name: "token assignment", input: `token: "plain-token"`, want: `token: "[REDACTED]"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RedactText(test.input)
			if got != test.want {
				t.Fatalf("redacted=%q, want %q", got, test.want)
			}
			if strings.Contains(got, "secret") || strings.Contains(got, "plain-") {
				t.Fatalf("credential leaked: %q", got)
			}
		})
	}
}

func TestTextStreamSanitizerRedactsCredentialsAcrossChunksAndResets(t *testing.T) {
	var sanitizer TextStreamSanitizer
	first, _ := sanitizer.SanitizeChunk("prefix password=", false, MaxEventBytes)
	second, _ := sanitizer.SanitizeChunk("split-password\nvisible", false, MaxEventBytes)
	got := first + second
	if strings.Contains(got, "split-password") || !strings.Contains(got, "password=[REDACTED]") || !strings.Contains(got, "visible") {
		t.Fatalf("cross-chunk redaction=%q", got)
	}

	_, _ = sanitizer.SanitizeChunk("token=", false, MaxEventBytes)
	final, _ := sanitizer.SanitizeChunk("", true, MaxEventBytes)
	if strings.Contains(final, "token=") {
		t.Fatalf("incomplete credential marker leaked at stream end: %q", final)
	}
	next, _ := sanitizer.SanitizeChunk("next task output", false, MaxEventBytes)
	if next != "next task output" {
		t.Fatalf("sanitizer state was not reset: %q", next)
	}

	var completeCredential TextStreamSanitizer
	firstComplete, _ := completeCredential.SanitizeChunk("password=complete-secret", false, MaxEventBytes)
	secondComplete, _ := completeCredential.SanitizeChunk(" next output", false, MaxEventBytes)
	if got := firstComplete + secondComplete; got != "password=[REDACTED] next output" {
		t.Fatalf("complete credential consumed following output: %q", got)
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
