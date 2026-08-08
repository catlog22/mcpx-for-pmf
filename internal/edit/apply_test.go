package edit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyReplacementsMultipleNonContiguous(t *testing.T) {
	dir := t.TempDir()
	path := "app.txt"
	original := "line1\ntitle: old\nline3\ncolor: red\nline5\n"
	if err := os.WriteFile(filepath.Join(dir, path), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	base := hashBytes([]byte(original))
	result, err := ApplyBatch(BatchRequest{
		WorkspaceRoot: dir,
		Edits: []FileEdit{{
			Path: path, Operation: OpUpdate, BaseSHA256: base,
			Replacements: []Replacement{
				{Match: "title: old", Replacement: "title: new"},
				{Match: "color: red", Replacement: "color: blue"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\ntitle: new\nline3\ncolor: blue\nline5\n"
	if string(got) != want {
		t.Fatalf("content=%q want %q", got, want)
	}
	if result.TotalChangedLines != 4 { // 2 deleted + 2 added
		t.Fatalf("changed lines=%d want 4\ndiff:\n%s", result.TotalChangedLines, result.DiffSummary)
	}
	if !strings.Contains(result.DiffSummary, "title: new") {
		t.Fatalf("diff missing new title: %s", result.DiffSummary)
	}
}

func TestMatchNotFoundAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	path := "f.txt"
	original := "aaa\nbbb\naaa\n"
	_ = os.WriteFile(filepath.Join(dir, path), []byte(original), 0o644)

	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "zzz", Replacement: "y"}},
	}}})
	if !errors.Is(err, ErrMatchNotFound) {
		t.Fatalf("want ErrMatchNotFound, got %v", err)
	}

	_, err = ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "aaa", Replacement: "x"}},
	}}})
	if !errors.Is(err, ErrMatchAmbiguous) {
		t.Fatalf("want ErrMatchAmbiguous, got %v", err)
	}
}

func TestStaleRevision(t *testing.T) {
	dir := t.TempDir()
	path := "f.txt"
	_ = os.WriteFile(filepath.Join(dir, path), []byte("hello\n"), 0o644)
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate, BaseSHA256: "sha256:deadbeef",
		Replacements: []Replacement{{Match: "hello", Replacement: "hi"}},
	}}})
	var ae *ApplyError
	if !errors.As(err, &ae) || ae.Code != "STALE_REVISION" {
		t.Fatalf("want STALE_REVISION, got %v", err)
	}
	if ae.Current == "" {
		t.Fatal("expected current sha")
	}
}

func TestDeleteRejectsSymlinkAndPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{Path: "link.txt", Operation: OpDelete}}})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Code != "SYMLINK_NOT_ALLOWED" {
		t.Fatalf("delete symlink error=%v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was affected: %v", err)
	}
}

func TestTooManyChangesBoundary(t *testing.T) {
	dir := t.TempDir()
	path := "big.txt"
	// 500 lines of "a"
	var old, neu strings.Builder
	for i := 0; i < 500; i++ {
		old.WriteString("a\n")
		neu.WriteString("b\n")
	}
	_ = os.WriteFile(filepath.Join(dir, path), []byte(old.String()), 0o644)

	// 500 del + 500 add = 1000 — allowed
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate, Content: neu.String(),
	}}})
	if err != nil {
		t.Fatalf("1000 lines should pass: %v", err)
	}

	// Reset and try 501+501 = 1002
	var old2, neu2 strings.Builder
	for i := 0; i < 501; i++ {
		old2.WriteString("a\n")
		neu2.WriteString("b\n")
	}
	_ = os.WriteFile(filepath.Join(dir, path), []byte(old2.String()), 0o644)
	_, err = ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate, Content: neu2.String(),
	}}})
	if !errors.Is(err, ErrTooManyChanges) {
		t.Fatalf("want ErrTooManyChanges, got %v", err)
	}
	// File must be unchanged
	got, _ := os.ReadFile(filepath.Join(dir, path))
	if string(got) != old2.String() {
		t.Fatal("file should not be written on too-many-changes")
	}
}

func TestTooManyChangesExact99910001001(t *testing.T) {
	makeContent := func(lines int, value byte) string {
		var b strings.Builder
		for i := 0; i < lines; i++ {
			b.WriteByte(value)
			b.WriteByte('\n')
		}
		return b.String()
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "boundary.txt")
	for _, tc := range []struct {
		name  string
		lines int
		want  error
	}{
		{name: "999", lines: 999},
		{name: "1000", lines: 1000},
		{name: "1001", lines: 1001, want: ErrTooManyChanges},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := makeContent(tc.lines, 'a')
			if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := ApplyBatch(BatchRequest{
				WorkspaceRoot: dir,
				Edits:         []FileEdit{{Path: "boundary.txt", Operation: OpUpdate, Content: ""}},
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want == nil {
				if result.TotalChangedLines != tc.lines {
					t.Fatalf("changed lines=%d want=%d", result.TotalChangedLines, tc.lines)
				}
				return
			}
			var applyErr *ApplyError
			if !errors.As(err, &applyErr) || applyErr.ChangedLines != tc.lines {
				t.Fatalf("apply error=%+v want changed lines=%d", applyErr, tc.lines)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != old {
				t.Fatal("file changed after over-limit rejection")
			}
		})
	}
}

func TestBatchTwoFilesLineCap(t *testing.T) {
	dir := t.TempDir()
	makeFile := func(name string, n int, ch byte) {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(ch)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644)
	}
	makeFile("a.txt", 300, 'a')
	makeFile("b.txt", 300, 'x')
	var na, nb strings.Builder
	for i := 0; i < 300; i++ {
		na.WriteString("b\n")
		nb.WriteString("y\n")
	}
	// 300*2 + 300*2 = 1200 > 1000
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{
		{Path: "a.txt", Operation: OpUpdate, Content: na.String()},
		{Path: "b.txt", Operation: OpUpdate, Content: nb.String()},
	}})
	if !errors.Is(err, ErrTooManyChanges) {
		t.Fatalf("want ErrTooManyChanges, got %v", err)
	}
}

func TestCreateAndDelete(t *testing.T) {
	dir := t.TempDir()
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: "new.txt", Operation: OpCreate, Content: "hello\nworld\n",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(got) != "hello\nworld\n" {
		t.Fatalf("got %q", got)
	}
	_, err = ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: "new.txt", Operation: OpDelete,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("expected deleted")
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	content := []byte("rename me\n")
	if err := os.WriteFile(oldPath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: "old.txt", NewPath: "new.txt", Operation: OpRename, BaseSHA256: hashBytes(content),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].NewPath != "new.txt" {
		t.Fatalf("rename result=%+v", result.Results)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "new.txt")); err != nil || string(got) != string(content) {
		t.Fatalf("new content=%q err=%v", got, err)
	}
}

func TestCountChangedLines(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n line\n-old\n+new\n"
	if n := CountChangedLines(diff); n != 2 {
		t.Fatalf("count=%d want 2", n)
	}
}

func TestUnifiedDiffUsesBoundedContextualHunks(t *testing.T) {
	oldLines := make([]string, 200)
	newLines := make([]string, 200)
	for index := range oldLines {
		oldLines[index] = fmt.Sprintf("line-%03d", index+1)
		newLines[index] = oldLines[index]
	}
	newLines[99] = "line-100 changed"
	newLines[189] = "line-190 changed"

	diff, changed := UnifiedDiff("large.txt", strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n")
	if changed != 4 || CountChangedLines(diff) != 4 {
		t.Fatalf("changed=%d count=%d diff=%s", changed, CountChangedLines(diff), diff)
	}
	if strings.Count(diff, "@@ ") != 2 {
		t.Fatalf("distant edits must render as two contextual hunks: %s", diff)
	}
	if strings.Contains(diff, "line-001") || strings.Contains(diff, "line-200") {
		t.Fatalf("unrelated distant context leaked into diff: %s", diff)
	}
	if !strings.Contains(diff, " line-097") || !strings.Contains(diff, " line-103") || !strings.Contains(diff, "-line-100") || !strings.Contains(diff, "+line-100 changed") {
		t.Fatalf("first hunk context/change missing: %s", diff)
	}
	if len(diff) > 1024 {
		t.Fatalf("small edits in a large file produced an oversized diff: %d bytes", len(diff))
	}
}

func TestPreserveCRLF(t *testing.T) {
	dir := t.TempDir()
	path := "win.txt"
	original := "a\r\nb\r\nc\r\n"
	_ = os.WriteFile(filepath.Join(dir, path), []byte(original), 0o644)
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "b", Replacement: "B"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, path))
	if !strings.Contains(string(got), "\r\n") {
		t.Fatalf("expected CRLF preserved, got %q", got)
	}
	if !strings.Contains(string(got), "B") {
		t.Fatalf("replacement missing: %q", got)
	}
}

func TestPreserveBOMLineEndingAndFinalNewline(t *testing.T) {
	dir := t.TempDir()
	path := "bom.txt"
	original := append([]byte{0xef, 0xbb, 0xbf}, []byte("one\r\ntwo\r\nthree")...)
	if err := os.WriteFile(filepath.Join(dir, path), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "two", Replacement: "TWO"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xef, 0xbb, 0xbf}, []byte("one\r\nTWO\r\nthree")...)
	if string(got) != string(want) {
		t.Fatalf("format changed: got %q want %q", got, want)
	}
}

func TestPreserveUTF16LEBOM(t *testing.T) {
	dir := t.TempDir()
	path := "utf16.txt"
	original := append([]byte{0xff, 0xfe}, encodeUTF16("one\r\ntwo\r\n", binary.LittleEndian)...)
	if err := os.WriteFile(filepath.Join(dir, path), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "two", Replacement: "TWO"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xff, 0xfe}, encodeUTF16("one\r\nTWO\r\n", binary.LittleEndian)...)
	if string(got) != string(want) {
		t.Fatalf("UTF-16 format changed: got %x want %x", got, want)
	}
}

func TestPreserveUTF16MixedLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := "utf16-mixed.txt"
	original := append([]byte{0xff, 0xfe}, encodeUTF16("one\r\ntwo\nthree\rfour", binary.LittleEndian)...)
	if err := os.WriteFile(filepath.Join(dir, path), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "three", Replacement: "THREE"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xff, 0xfe}, encodeUTF16("one\r\ntwo\nTHREE\rfour", binary.LittleEndian)...)
	if string(got) != string(want) {
		t.Fatalf("UTF-16 mixed format changed: got %x want %x", got, want)
	}
}

func TestPreserveMixedLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := "mixed.txt"
	original := []byte("one\r\ntwo\nthree\rfour")
	if err := os.WriteFile(filepath.Join(dir, path), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: path, Operation: OpUpdate,
		Replacements: []Replacement{{Match: "three", Replacement: "THREE"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("one\r\ntwo\nTHREE\rfour")
	if string(got) != string(want) {
		t.Fatalf("mixed line endings changed: got %q want %q", got, want)
	}
}

func TestFinalNewlineChangeProducesDiff(t *testing.T) {
	diff, changed := UnifiedDiff("demo.txt", "same\n", "same")
	if changed != 2 || CountChangedLines(diff) != 2 {
		t.Fatalf("final newline diff changed=%d count=%d diff=%q", changed, CountChangedLines(diff), diff)
	}
	if !strings.Contains(diff, "-same") || !strings.Contains(diff, "+same") || !strings.Contains(diff, "No newline") {
		t.Fatalf("final newline diff is not auditable: %q", diff)
	}
}

func TestRejectDuplicateBatchPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{
		{Path: "duplicate.txt", Operation: OpUpdate, Replacements: []Replacement{{Match: "one", Replacement: "two"}}},
		{Path: "duplicate.txt", Operation: OpUpdate, Replacements: []Replacement{{Match: "one", Replacement: "three"}}},
	}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate path error=%v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "one\n" {
		t.Fatalf("duplicate batch wrote file: %q", got)
	}
}

func TestRejectOverlappingReplacements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlap.txt")
	original := []byte("ababa\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{
		Path: "overlap.txt", Operation: OpUpdate,
		Replacements: []Replacement{
			{Match: "aba", Replacement: "x"},
			{Match: "bab", Replacement: "y"},
		},
	}}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("overlap error=%v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("overlap batch wrote file: %q", got)
	}
}
