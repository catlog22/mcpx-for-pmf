package observation

import (
	"strings"
	"testing"
)

func TestParseDiffDocumentTracksHunksAndSourceLines(t *testing.T) {
	document := parseDiffDocument("--- a/demo.go\n+++ b/demo.go\n@@ -10,2 +20,3 @@ func demo()\n context\n-old\n+new\n+another\n\\ No newline at end of file\n")
	if document.hunks != 1 || document.added != 2 || document.removed != 1 {
		t.Fatalf("diff stats=%+v", document)
	}
	if got := document.lines[3]; got.kind != diffLineContext || got.oldLine != 10 || got.newLine != 20 {
		t.Fatalf("context line=%+v", got)
	}
	if got := document.lines[4]; got.kind != diffLineRemoved || got.oldLine != 11 || got.hasNew {
		t.Fatalf("removed line=%+v", got)
	}
	if got := document.lines[5]; got.kind != diffLineAdded || got.newLine != 21 || got.hasOld {
		t.Fatalf("added line=%+v", got)
	}
	if got := document.lines[len(document.lines)-1]; got.kind != diffLineNoNewline {
		t.Fatalf("newline marker=%+v", got)
	}
}

func TestDiffStylesDistinguishLineKinds(t *testing.T) {
	added := styleRenderedDiffLine("  21 | +new", diffLineAdded, ColorModeTrueColor)
	if !strings.Contains(added, ansiDiffAddedForeground) || !strings.Contains(added, ansiDiffAddedBackground) {
		t.Fatalf("added style=%q", added)
	}
	removed := styleRenderedDiffLine("  20 | -old", diffLineRemoved, ColorModeTrueColor)
	if !strings.Contains(removed, ansiDiffRemovedForeground) || !strings.Contains(removed, ansiDiffRemovedBackground) {
		t.Fatalf("removed style=%q", removed)
	}
	hunk := styleRenderedDiffLine("    | @@ -1 +1 @@", diffLineHunkHeader, ColorModeTrueColor)
	if !strings.Contains(hunk, ansiDiffHunkForeground) || !strings.Contains(hunk, ansiBold) {
		t.Fatalf("hunk style=%q", hunk)
	}
	marker := styleRenderedDiffLine(`    | \ No newline at end of file`, diffLineNoNewline, ColorModeANSI16)
	if !strings.Contains(marker, ansiUnderline) || !strings.Contains(marker, ansiYellow) {
		t.Fatalf("newline marker style=%q", marker)
	}
}

func TestDiffDocumentCacheIsBounded(t *testing.T) {
	cache := newDiffDocumentCache()
	first := cache.get("@@ -1 +1 @@\n-old\n+new")
	second := cache.get("@@ -1 +1 @@\n-old\n+new")
	if len(cache.documents) != 1 || first.added != second.added {
		t.Fatalf("cache=%d first=%+v second=%+v", len(cache.documents), first, second)
	}
	for index := 0; index < 80; index++ {
		cache.get("@@ -1 +1 @@\n-old\n+new-" + string(rune('a'+index)))
	}
	if len(cache.documents) > 64 {
		t.Fatalf("cache size=%d, want <=64", len(cache.documents))
	}
}
