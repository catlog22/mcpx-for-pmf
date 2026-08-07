package prompts

import "testing"

func TestDescriptions(t *testing.T) {
	m, err := Descriptions()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"change", "session", "source_read", "artifact"} {
		if m[name] == "" {
			t.Fatalf("missing description for %s", name)
		}
	}
}
