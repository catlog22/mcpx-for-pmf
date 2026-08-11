package edit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRangeUpdatePreservesCRLFAndSupportsDeletion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	original := []byte("one\r\ntwo\r\nthree\r\nfour\r\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := ApplyBatch(BatchRequest{WorkspaceRoot: root, Edits: []FileEdit{{
		Path: "demo.txt", Operation: OpUpdate, BaseSHA256: hashBytes(original),
		Range: &LineRange{StartLine: 2, EndLine: 3, Replacement: "TWO\nTHREE"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 1 || first.Results[0].NewSHA256 == "" {
		t.Fatalf("range result=%+v", first)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(updated), "one\r\nTWO\r\nTHREE\r\nfour\r\n"; got != want {
		t.Fatalf("CRLF range update=%q want=%q", got, want)
	}

	second, err := ApplyBatch(BatchRequest{WorkspaceRoot: root, Edits: []FileEdit{{
		Path: "demo.txt", Operation: OpUpdate, BaseSHA256: hashBytes(updated),
		Range: &LineRange{StartLine: 2, EndLine: 3, Replacement: ""},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Results[0].ChangedLines == 0 {
		t.Fatalf("range deletion did not report changes: %+v", second)
	}
	deleted, _ := os.ReadFile(path)
	if got, want := string(deleted), "one\r\nfour\r\n"; got != want {
		t.Fatalf("range deletion=%q want=%q", got, want)
	}
}

func TestRangeUpdateRequiresCurrentRevisionAndBounds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	original := []byte("one\ntwo\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: root, Edits: []FileEdit{{
		Path: "demo.txt", Operation: OpUpdate,
		Range: &LineRange{StartLine: 1, EndLine: 1, Replacement: "ONE"},
	}}})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Code != "INVALID_INPUT" {
		t.Fatalf("missing base range error=%v", err)
	}

	_, err = ApplyBatch(BatchRequest{WorkspaceRoot: root, Edits: []FileEdit{{
		Path: "demo.txt", Operation: OpUpdate, BaseSHA256: "sha256:stale",
		Range: &LineRange{StartLine: 1, EndLine: 1, Replacement: "ONE"},
	}}})
	if !errors.As(err, &applyErr) || applyErr.Code != "STALE_REVISION" {
		t.Fatalf("stale range error=%v", err)
	}

	_, err = ApplyBatch(BatchRequest{WorkspaceRoot: root, Edits: []FileEdit{{
		Path: "demo.txt", Operation: OpUpdate, BaseSHA256: hashBytes(original),
		Range: &LineRange{StartLine: 2, EndLine: 3, Replacement: "x"},
	}}})
	if !errors.As(err, &applyErr) || applyErr.Code != "RANGE_OUT_OF_BOUNDS" {
		t.Fatalf("out-of-bounds range error=%v", err)
	}
}
